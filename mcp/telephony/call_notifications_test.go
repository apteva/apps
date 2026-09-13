package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func notificationServer(t *testing.T, a *App) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for _, route := range a.HTTPRoutes() {
		mux.HandleFunc(route.Pattern, route.Handler)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}
func readNotificationStream(t *testing.T, response *http.Response) <-chan string {
	t.Helper()
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream status %d", response.StatusCode)
	}
	events := make(chan string, 16)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				events <- strings.TrimPrefix(scanner.Text(), "data: ")
			}
		}
	}()
	return events
}
func delegatedNotificationRequest(url string, identity phoneIdentity) *http.Request {
	r, _ := http.NewRequest("GET", url, nil)
	r.Header.Set("X-Apteva-Project-ID", "project-a")
	r.Header.Set("X-Apteva-Issuer-App", identity.IssuerApp)
	r.Header.Set("X-Apteva-Issuer-Install-ID", identity.IssuerInstallID)
	r.Header.Set("X-Apteva-Subject-Type", identity.SubjectType)
	r.Header.Set("X-Apteva-Subject-ID", identity.SubjectID)
	r.Header.Set("X-Apteva-Organization-ID", identity.OrganizationID)
	r.Header.Set("X-Apteva-Scopes", `[{"type":"app_user","app":"telephony","actions":["call.read"]}]`)
	return r
}
func TestCallNotificationsFilterOtherUsersAndRevoke(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	policy := phoneTestPolicy(t, a)
	server := notificationServer(t, a)
	req := delegatedNotificationRequest(server.URL+"/calls/events", phoneTestIdentity("alice"))
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	events := readNotificationStream(t, resp)
	receiveSoon(t, events)
	other := testCall("other-user-call", "pending")
	other.Direction = "inbound"
	other.ProjectID = "project-a"
	other.PeerKind = peerKindHuman
	other.RoutingDestinationID = "support"
	if e = a.db().insertCall(other); e != nil {
		t.Fatal(e)
	}
	a.callChanges.notify("project-a")
	select {
	case event := <-events:
		t.Fatalf("other user's call leaked a notification: %s", event)
	case <-time.After(50 * time.Millisecond):
	}
	phoneTestCall(t, a, "own-call", "pending")
	a.callChanges.notify("project-a")
	event := receiveSoon(t, events)
	if event != `{"type":"calls.changed"}` || strings.Contains(event, "own-call") {
		t.Fatal(event)
	}
	for i := range policy.Users {
		if policy.Users[i].Identity.SubjectID == "alice" {
			policy.Users[i].Enabled = false
		}
	}
	raw, _ := json.Marshal(policy)
	_, _ = a.db().db.Exec(`UPDATE telephony_access_policies SET policy_json=? WHERE project_id='project-a'`, string(raw))
	a.callChanges.notify("project-a")
	if event := receiveSoon(t, events); event != `{"type":"access.revoked"}` {
		t.Fatal(event)
	}
}
func TestCallNotificationsOnlineSessionRevocationAndProjectIsolation(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	policy := phoneTestPolicy(t, a)
	var revoked atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if revoked.Load() {
			http.Error(w, "revoked", 401)
			return
		}
		writeJSON(w, map[string]any{"user": map[string]any{"id": "alice", "organization_id": "org-1", "project_id": "project-a"}})
	}))
	defer provider.Close()
	policy.Providers = []phoneAuthProvider{{ID: "auth", IssuerApp: "auth", IssuerInstallID: "11", Format: "apteva-auth", URL: provider.URL, Actions: []string{"call.read"}}}
	if result := phoneTestRequest(a, nil, "PUT", "/access/policy", policy); result.Code != 200 {
		t.Fatal(result.Body)
	}
	server := notificationServer(t, a)
	req, _ := http.NewRequest("GET", server.URL+"/user/calls/events?auth_provider=auth", nil)
	req.Header.Set("Authorization", "Bearer valid-test-session")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	events := readNotificationStream(t, resp)
	receiveSoon(t, events)
	a.callChanges.notify("other-project")
	select {
	case e := <-events:
		t.Fatal(e)
	case <-time.After(30 * time.Millisecond):
	}
	revoked.Store(true)
	a.callChanges.notify("project-a")
	if event := receiveSoon(t, events); event != `{"type":"access.revoked"}` {
		t.Fatal(event)
	}
	forged := delegatedNotificationRequest(server.URL+"/user/calls/events?auth_provider=auth", phoneTestIdentity("boss"))
	response, e := http.DefaultClient.Do(forged)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatalf("missing session accepted: %d", response.StatusCode)
	}
}
func TestNotificationHubIsBoundedAndCancelable(t *testing.T) {
	var hub callChangeHub
	var closes []func()
	for range callProjectStreamLimit {
		_, close, e := hub.subscribe("p")
		if e != nil {
			t.Fatal(e)
		}
		closes = append(closes, close)
	}
	if _, _, e := hub.subscribe("p"); e == nil {
		t.Fatal("unbounded project subscribers")
	}
	for _, close := range closes {
		close()
		close()
	}
	if hub.count != 0 {
		t.Fatal("subscriber leak")
	}
	ch, close, e := hub.subscribe("p")
	if e != nil {
		t.Fatal(e)
	}
	defer close()
	for range 10000 {
		hub.notify("p")
	}
	if len(ch) != 1 {
		t.Fatal("notification burst not coalesced")
	}
}
func TestNotificationDisconnectCleansSubscription(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	server := notificationServer(t, a)
	ctx, cancel := context.WithCancel(context.Background())
	req := delegatedNotificationRequest(server.URL+"/calls/events", phoneTestIdentity("alice")).WithContext(ctx)
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	events := readNotificationStream(t, resp)
	receiveSoon(t, events)
	cancel()
	resp.Body.Close()
	until := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(until) {
		a.callChanges.mu.Lock()
		count := a.callChanges.count
		a.callChanges.mu.Unlock()
		if count == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("disconnected subscriber retained")
}
