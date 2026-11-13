package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

type phoneAdminFixture struct {
	app    *App
	ctx    *sdk.AppCtx
	caller *sdk.Caller
	policy phonePolicy
}

func newPhoneAdminFixture(t *testing.T) *phoneAdminFixture {
	t.Helper()
	ctx := softphoneTestCtx(t)
	app := &App{installID: 42}
	provider := phoneAuthProvider{ID: "login", IssuerApp: "auth", IssuerInstallID: "11", URL: "https://auth.example.test/me", Format: "apteva-auth", Actions: append([]string{}, ordinaryPhoneActions...)}
	delegation := phoneDelegation{AppInstallID: 91, AppName: "functions", Permissions: append([]string{}, phoneAdminPermissions...), DestinationPrefixes: []string{"desk-"}, Authorities: []phoneAuthority{{"auth", "11", "org-1", "user"}}, Actions: append([]string{}, ordinaryPhoneActions...), OutboundNumbers: []string{"+13502231050"}}
	policy := phonePolicy{Providers: []phoneAuthProvider{provider}, Delegations: []phoneDelegation{delegation}}
	w := phoneTestRequest(app, nil, "PUT", "/access/policy", policy)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}
	caller := &sdk.Caller{AppInstallID: 91, AppName: "functions", ProjectID: "project-a", DefaultEffect: "deny"}
	for _, permission := range phoneAdminPermissions {
		caller.Grants = append(caller.Grants, sdk.Grant{Effect: "allow", Permission: permission, Resource: "*"})
	}
	return &phoneAdminFixture{app, ctx.WithProject("project-a"), caller, policy}
}
func (f *phoneAdminFixture) call(t *testing.T, operation string, args map[string]any) map[string]any {
	t.Helper()
	for _, tool := range f.app.MCPTools() {
		if tool.Name == "telephony_"+operation {
			out, err := tool.HandlerCtx(sdk.WithCaller(context.Background(), f.caller), f.ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			value, ok := out.(map[string]any)
			if !ok {
				t.Fatalf("invalid response %T", out)
			}
			return value
		}
	}
	t.Fatal("tool not registered", operation)
	return nil
}
func adminData(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	if response["ok"] != true {
		t.Fatalf("administration failed: %v", response)
	}
	raw, err := json.Marshal(response["data"])
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}
func adminCode(t *testing.T, response map[string]any, code string) {
	t.Helper()
	raw, _ := json.Marshal(response)
	var value struct {
		OK    bool               `json:"ok"`
		Error resourceAdminError `json:"error"`
	}
	if json.Unmarshal(raw, &value) != nil || value.OK || value.Error.Code != code {
		t.Fatalf("wanted %s, got %s", code, raw)
	}
}
func adminDestinationArgs(id, user string) map[string]any {
	return map[string]any{"id": id, "name": "Desk", "kind": "browser", "config": map[string]any{"capacity": destinationCapacity{Identity: phoneTestIdentity(user), Limit: 1}}, "enabled": true, "expected_revision": nil, "idempotency_key": "destination-" + id}
}
func adminGrantArgs(id, destination, user string, actions ...string) map[string]any {
	return map[string]any{"id": id, "principal": phoneTestIdentity(user), "resource": phoneResource{"destination", destination}, "actions": actions, "enabled": true, "expected_revision": nil, "idempotency_key": "grant-" + id}
}
func (f *phoneAdminFixture) provision(t *testing.T, id, user string, actions ...string) map[string]any {
	t.Helper()
	adminData(t, f.call(t, "destinations_upsert", adminDestinationArgs(id, user)))
	return adminData(t, f.call(t, "grants_upsert", adminGrantArgs("grant-"+id, id, user, actions...)))
}

func TestPhoneResourceAdminProvisionAndGranularRevocation(t *testing.T) {
	f := newPhoneAdminFixture(t)
	grant := f.provision(t, "desk-alice", "alice", "call.read", "call.answer", "call.attach")
	identity := phoneTestIdentity("alice")
	row := phoneTestCall(t, f.app, "managed-call", "pending")
	if _, err := f.ctx.AppDB().Exec(`UPDATE calls SET routing_destination_id='desk-alice' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	row.RoutingDestinationID = "desk-alice"
	p, err := f.app.phonePrincipal("project-a", identity)
	if err != nil {
		t.Fatal(err)
	}
	if !p.forAction("call.answer").Destinations["desk-alice"] || p.forAction("call.hangup").Destinations["desk-alice"] || p.Supervisor {
		t.Fatalf("incorrect action grants: %+v", p)
	}
	w := phoneTestRequest(f.app, &identity, "POST", "/softphone/answer/managed-call", map[string]any{})
	if w.Code != 200 {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	var session softphoneSession
	if err = json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	stored, err := f.app.db().findCall(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !f.app.validPhoneMedia(stored, session.SessionToken) {
		t.Fatal("provisioned media denied")
	}
	if w = phoneTestRequest(f.app, &identity, "POST", "/calls/managed-call/hangup", map[string]any{}); w.Code < 400 {
		t.Fatal("ungranted hangup allowed")
	}
	// Independent grants union their contributions. Removing one keeps the other.
	second := adminData(t, f.call(t, "grants_upsert", adminGrantArgs("second-grant", "desk-alice", "alice", "call.read", "call.attach")))
	adminData(t, f.call(t, "grants_delete", map[string]any{"id": grant["id"], "expected_revision": grant["revision"], "idempotency_key": "delete-first"}))
	if !f.app.validPhoneMedia(stored, session.SessionToken) {
		t.Fatal("another valid grant was lost")
	}
	adminData(t, f.call(t, "grants_delete", map[string]any{"id": second["id"], "expected_revision": second["revision"], "idempotency_key": "delete-second"}))
	if f.app.validPhoneMedia(stored, session.SessionToken) {
		t.Fatal("revoked media restored")
	}
	if w = phoneTestRequest(f.app, &identity, "POST", "/softphone/renew/managed-call", map[string]any{"session_token": session.SessionToken}); w.Code < 400 {
		t.Fatal("revoked renewal allowed")
	}
	if w = phoneTestRequest(f.app, &identity, "GET", "/calls", nil); w.Code < 400 {
		t.Fatal("revoked read allowed")
	}
	var owner string
	if err = f.ctx.AppDB().QueryRow(`SELECT principal FROM telephony_call_owners WHERE call_id=?`, row.ID).Scan(&owner); err != nil || owner != identity.key() {
		t.Fatal("revocation altered call ownership", err)
	}
}
func TestPhoneResourceAdminRetryConcurrencyAndRevisions(t *testing.T) {
	f := newPhoneAdminFixture(t)
	args := adminDestinationArgs("desk-concurrent", "alice")
	results := make(chan map[string]any, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- f.app.resourceAdmin(sdk.WithCaller(context.Background(), f.caller), f.ctx, "destinations_upsert", args)
		}()
	}
	wg.Wait()
	close(results)
	revision := ""
	replayed := 0
	for result := range results {
		data := adminData(t, result)
		if revision == "" {
			revision = data["revision"].(string)
		}
		if data["revision"] != revision {
			t.Fatal("retry changed revision")
		}
		if result["replayed"] == true {
			replayed++
		}
	}
	if replayed != 7 {
		t.Fatalf("wanted seven replays, got %d", replayed)
	}
	altered := adminDestinationArgs("desk-concurrent", "alice")
	altered["name"] = "Changed"
	adminCode(t, f.call(t, "destinations_upsert", altered), "idempotency_conflict")
	altered["idempotency_key"] = "new-key"
	adminCode(t, f.call(t, "destinations_upsert", altered), "revision_conflict")
	altered["expected_revision"] = revision
	first := adminData(t, f.call(t, "destinations_upsert", altered))
	if first["revision"] == revision {
		t.Fatal("revision did not advance")
	}
	altered["idempotency_key"] = "stale"
	adminCode(t, f.call(t, "destinations_upsert", altered), "revision_conflict")
	// The legacy operator writer advances the exact same revision.
	if _, err := f.app.saveRoutingDestination("project-a", "desk-concurrent", "Operator edit", "browser", args["config"], true); err != nil {
		t.Fatal(err)
	}
	altered["expected_revision"] = first["revision"]
	altered["idempotency_key"] = "after-operator"
	adminCode(t, f.call(t, "destinations_upsert", altered), "revision_conflict")
	for _, id := range []string{"g-one", "g-two"} {
		adminData(t, f.call(t, "grants_upsert", adminGrantArgs(id, "desk-concurrent", "alice", "call.read")))
	}
	updates := make([]map[string]any, 2)
	for i, id := range []string{"g-one", "g-two"} {
		g := adminData(t, f.call(t, "grants_get", map[string]any{"id": id}))
		updates[i] = adminGrantArgs(id, "desk-concurrent", "alice", "call.read", "call.answer")
		updates[i]["expected_revision"] = g["revision"]
		updates[i]["idempotency_key"] = "update-" + id
	}
	results = make(chan map[string]any, 2)
	for _, update := range updates {
		wg.Add(1)
		go func(args map[string]any) {
			defer wg.Done()
			results <- f.app.resourceAdmin(sdk.WithCaller(context.Background(), f.caller), f.ctx, "grants_upsert", args)
		}(update)
	}
	wg.Wait()
	close(results)
	for result := range results {
		adminData(t, result)
	}
	for _, id := range []string{"g-one", "g-two"} {
		g := adminData(t, f.call(t, "grants_get", map[string]any{"id": id}))
		if len(g["actions"].([]any)) != 2 {
			t.Fatal("concurrent grant update lost")
		}
	}
	var count int
	if err := f.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM telephony_admin_audit WHERE operation='destinations_upsert'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("duplicate audit: %d %v", count, err)
	}
}
func TestPhoneResourceAdminIdentityAndDelegationBoundary(t *testing.T) {
	f := newPhoneAdminFixture(t)
	args := adminDestinationArgs("desk-alice", "alice")
	for _, caller := range []*sdk.Caller{nil, {AgentID: 7, ProjectID: "project-a"}, {AppInstallID: 91, AppName: "functions"}, {AppInstallID: 91, AppName: "functions", ProjectID: "other"}, {AppInstallID: 92, AppName: "functions", ProjectID: "project-a"}} {
		adminCode(t, f.app.resourceAdmin(sdk.WithCaller(context.Background(), caller), f.ctx, "destinations_upsert", args), "forbidden")
	}
	bad := adminDestinationArgs("foreign-desk", "alice")
	adminCode(t, f.call(t, "destinations_upsert", bad), "forbidden")
	bad = adminDestinationArgs("desk-foreign", "alice")
	bad["config"] = map[string]any{"capacity": destinationCapacity{Identity: phoneIdentity{"auth", "foreign", "user", "alice", "org-1"}, Limit: 1}}
	adminCode(t, f.call(t, "destinations_upsert", bad), "forbidden")
	f.caller.SubjectID = "manager"
	f.caller.SubjectType = "user"
	f.provision(t, "desk-alice", "alice", "call.read") // Delegated subject does not erase verified service authority.
	adminCode(t, f.call(t, "grants_upsert", adminGrantArgs("spy", "desk-alice", "alice", "call.listen")), "unsupported_action")
	args = adminGrantArgs("wrong-user", "desk-alice", "bob", "call.read")
	adminCode(t, f.call(t, "grants_upsert", args), "invalid_reference")
	for _, tool := range f.app.MCPTools() {
		if tool.Name == "telephony_destinations_create" {
			out, err := tool.HandlerCtx(sdk.WithCaller(context.Background(), f.caller), f.ctx, map[string]any{"name": "Bypass", "kind": "browser"})
			if err != nil {
				t.Fatal(err)
			}
			adminCode(t, out.(map[string]any), "forbidden")
		}
	}
	for _, path := range []string{"/access/policy", "/routing/destinations/save"} {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		r.Header.Set("X-Apteva-Bound-Caller-Install-ID", "91")
		w := httptest.NewRecorder()
		if strings.HasPrefix(path, "/access") {
			f.app.handlePhoneAccess(w, r)
		} else {
			f.app.handleRouting(w, r)
		}
		if w.Code != 403 {
			t.Fatalf("HTTP bypass: %s %d", path, w.Code)
		}
	}
	// Revoking service delegation denies receipt replay as well as new writes.
	policy, err := f.app.phonePolicy("project-a")
	if err != nil {
		t.Fatal(err)
	}
	policy.Delegations = []phoneDelegation{}
	if w := phoneTestRequest(f.app, nil, "PUT", "/access/policy", policy); w.Code != 200 {
		t.Fatal(w.Body)
	}
	adminCode(t, f.call(t, "destinations_upsert", adminDestinationArgs("desk-alice", "alice")), "forbidden")
}
func TestPhoneResourceAdminLegacyDisableAndPolicyPreservation(t *testing.T) {
	f := newPhoneAdminFixture(t)
	f.provision(t, "desk-alice", "alice", "call.read", "call.answer", "call.attach")
	policy, err := f.app.phonePolicy("project-a")
	if err != nil {
		t.Fatal(err)
	}
	policy.Users = []phoneUser{{Identity: phoneTestIdentity("alice"), Enabled: false, phoneGrant: phoneGrant{Role: "user"}}}
	raw, _ := json.Marshal(policy)
	var body map[string]any
	json.Unmarshal(raw, &body)
	delete(body, "delegations")
	if w := phoneTestRequest(f.app, nil, "PUT", "/access/policy", body); w.Code != 200 {
		t.Fatal(w.Body)
	}
	policy, err = f.app.phonePolicy("project-a")
	if err != nil || len(policy.Delegations) != 1 {
		t.Fatal("omitted delegation erased", err)
	}
	if _, err = f.app.phonePrincipal("project-a", phoneTestIdentity("alice")); err == nil {
		t.Fatal("grant reenabled explicitly disabled user")
	}
}
func TestPhoneResourceAdminCursorAndTombstone(t *testing.T) {
	f := newPhoneAdminFixture(t)
	for i := range 3 {
		f.provision(t, fmt.Sprintf("desk-%d", i), fmt.Sprintf("user-%d", i), "call.read")
	}
	page := adminData(t, f.call(t, "grants_list", map[string]any{"limit": 1}))
	cursor := page["cursor"].(string)
	if cursor == "" {
		t.Fatal("missing cursor")
	}
	next := adminData(t, f.call(t, "grants_list", map[string]any{"limit": 1, "cursor": cursor}))
	if next["items"].([]any)[0].(map[string]any)["id"] == page["items"].([]any)[0].(map[string]any)["id"] {
		t.Fatal("repeated cursor item")
	}
	adminCode(t, f.call(t, "grants_list", map[string]any{"limit": 1, "cursor": cursor, "include_deleted": true}), "invalid_argument")
	adminCode(t, f.call(t, "grants_list", map[string]any{"cursor": cursor + "tampered"}), "invalid_argument")
	grant := adminData(t, f.call(t, "grants_get", map[string]any{"id": "grant-desk-0"}))
	adminData(t, f.call(t, "grants_delete", map[string]any{"id": grant["id"], "expected_revision": grant["revision"], "idempotency_key": "revoke"}))
	page = adminData(t, f.call(t, "grants_list", map[string]any{}))
	if len(page["items"].([]any)) != 2 {
		t.Fatal("deleted grant listed")
	}
	page = adminData(t, f.call(t, "grants_list", map[string]any{"include_deleted": true}))
	if len(page["items"].([]any)) != 3 {
		t.Fatal("tombstone lost")
	}
}

func TestPhoneResourceAdminOnlineLoginAfterProvisioning(t *testing.T) {
	f := newPhoneAdminFixture(t)
	var mu sync.Mutex
	revoked := false
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if revoked || r.Header.Get("Authorization") != "Bearer verified-login-session" {
			http.Error(w, "invalid login", 401)
			return
		}
		writeJSON(w, map[string]any{"user": map[string]any{"id": "alice", "organization_id": "org-1", "project_id": "project-a"}})
	}))
	defer provider.Close()
	t.Setenv("APTEVA_GATEWAY_URL", provider.URL)
	f.policy.Providers[0].URL = provider.URL
	if w := phoneTestRequest(f.app, nil, "PUT", "/access/policy", f.policy); w.Code != 200 {
		t.Fatal(w.Body)
	}
	f.provision(t, "desk-alice", "alice", "call.read", "call.answer", "call.attach")
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/user/softphone/access?auth_provider=login", nil)
		r.Header.Set("Authorization", "Bearer verified-login-session")
		r.Header.Set("X-Apteva-Subject-ID", "boss")
		w := httptest.NewRecorder()
		f.app.handleApplicationSession(w, r)
		return w
	}
	w := request()
	if w.Code != 200 || !strings.Contains(w.Body.String(), "desk-alice") || strings.Contains(w.Body.String(), "boss") {
		t.Fatalf("provisioned login: %d %s", w.Code, w.Body)
	}
	mu.Lock()
	revoked = true
	mu.Unlock()
	if w = request(); w.Code != 401 {
		t.Fatal("logged-out user restored access")
	}
}

func TestPhoneResourceAdminFailedAuditRollsBackMutation(t *testing.T) {
	f := newPhoneAdminFixture(t)
	if _, err := f.ctx.AppDB().Exec(`CREATE TRIGGER fail_admin_audit BEFORE INSERT ON telephony_admin_audit BEGIN SELECT RAISE(ABORT,'audit failed'); END`); err != nil {
		t.Fatal(err)
	}
	adminCode(t, f.call(t, "destinations_upsert", adminDestinationArgs("desk-atomic", "alice")), "storage_unavailable")
	for _, table := range []string{"routing_destinations", "telephony_admin_receipts"} {
		var count int
		if err := f.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial mutation in %s: %d %v", table, count, err)
		}
	}
}

func TestPhoneResourceAdminAttachmentCacheCannotRestoreRevokedAction(t *testing.T) {
	f := newPhoneAdminFixture(t)
	grant := f.provision(t, "desk-alice", "alice", "call.read", "call.attach")
	identity := phoneTestIdentity("alice")
	row := phoneTestCall(t, f.app, "cached-managed-call", "answered")
	row.RoutingDestinationID = "desk-alice"
	if _, err := f.ctx.AppDB().Exec(`UPDATE calls SET routing_destination_id=? WHERE id=?`, row.RoutingDestinationID, row.ID); err != nil {
		t.Fatal(err)
	}
	p, err := f.app.phonePrincipal(row.ProjectID, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.app.setPhoneOwner(&row, p, "desk-alice"); err != nil {
		t.Fatal(err)
	}
	guard := &phoneAttachmentGuard{ID: "attempt-acde"}
	session, err := f.app.issueGuardedPhoneSession(context.Background(), &row, p, audioSessionCorrelation{}, guard)
	if err != nil {
		t.Fatal(err)
	}
	update := adminGrantArgs(grant["id"].(string), "desk-alice", "alice", "call.read")
	update["expected_revision"], update["idempotency_key"] = grant["revision"], "remove-attach"
	adminData(t, f.call(t, "grants_upsert", update))
	if _, err = f.app.issueGuardedPhoneSession(context.Background(), &row, p, audioSessionCorrelation{}, guard); err == nil {
		t.Fatal("stale principal retrieved cached attachment")
	}
	if f.app.validPhoneMedia(&row, session.SessionToken) {
		t.Fatal("old media token survived attachment revocation")
	}
	if w := phoneTestRequest(f.app, &identity, "GET", "/calls", nil); w.Code != 200 {
		t.Fatalf("unrelated read grant lost: %d %s", w.Code, w.Body)
	}
}
func TestPhoneResourceAdminCompetingGrantWritesAndOwnership(t *testing.T) {
	f := newPhoneAdminFixture(t)
	grant := f.provision(t, "desk-alice", "alice", "call.read")
	results := make(chan map[string]any, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := adminGrantArgs("grant-desk-alice", "desk-alice", "alice", "call.read", "call.answer")
			args["expected_revision"], args["idempotency_key"] = grant["revision"], fmt.Sprintf("competing-%d", i)
			results <- f.app.resourceAdmin(sdk.WithCaller(context.Background(), f.caller), f.ctx, "grants_upsert", args)
		}(i)
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for response := range results {
		if response["ok"] == true {
			succeeded++
		} else {
			adminCode(t, response, "revision_conflict")
		}
	}
	if succeeded != 1 {
		t.Fatalf("wanted one successful write, got %d", succeeded)
	}
	policy, err := f.app.phonePolicy("project-a")
	if err != nil {
		t.Fatal(err)
	}
	other := policy.Delegations[0]
	other.AppInstallID = 92
	policy.Delegations = append(policy.Delegations, other)
	if w := phoneTestRequest(f.app, nil, "PUT", "/access/policy", policy); w.Code != 200 {
		t.Fatal(w.Body)
	}
	caller := *f.caller
	caller.AppInstallID = 92
	response := f.app.resourceAdmin(sdk.WithCaller(context.Background(), &caller), f.ctx, "grants_delete", map[string]any{"id": "grant-desk-alice", "expected_revision": grant["revision"], "idempotency_key": "foreign-delete"})
	adminCode(t, response, "forbidden")
}
func TestPhoneResourceAdminDestinationDisableStopsAdmission(t *testing.T) {
	f := newPhoneAdminFixture(t)
	f.provision(t, "desk-alice", "alice", "call.read", "call.answer", "call.attach")
	row := phoneTestCall(t, f.app, "disable-desk-call", "pending")
	if _, err := f.ctx.AppDB().Exec(`UPDATE calls SET routing_destination_id='desk-alice' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	dest := adminData(t, f.call(t, "destinations_get", map[string]any{"id": "desk-alice"}))
	args := adminDestinationArgs("desk-alice", "alice")
	args["expected_revision"], args["idempotency_key"], args["enabled"] = dest["revision"], "disable-desk", false
	adminData(t, f.call(t, "destinations_upsert", args))
	identity := phoneTestIdentity("alice")
	if w := phoneTestRequest(f.app, &identity, "POST", "/softphone/answer/disable-desk-call", map[string]any{}); w.Code < 400 {
		t.Fatal("disabled destination admitted a new answer")
	}
	stored, err := f.app.db().findCall(row.ID)
	if err != nil || stored.Status != "pending" {
		t.Fatal("destination disable ended or answered the carrier call", err)
	}
}

func TestPhoneResourceAdminOutboundRequiresDestinationAndOwnedNumber(t *testing.T) {
	f := newPhoneAdminFixture(t)
	platform := &answerPlatform{bindings: map[string]any{"carrier": 9}, credentials: &sdk.ConnectionCredentials{Slug: "twilio", Fields: map[string]string{"phone_number": "+13502231050", "auth_token": "test-auth-token"}}}
	base := sdk.NewAppCtxForTest(f.ctx.Manifest(), f.ctx.AppDB(), f.ctx.Config(), platform, f.ctx.Logger())
	ctx := base.WithProject("project-a")
	f.ctx, globalCtx = ctx, base
	f.provision(t, "desk-alice", "alice", "call.read", "call.attach", "call.dial", "call.hangup")
	args := adminGrantArgs("outbound-id", "desk-alice", "alice", "call.dial")
	args["resource"] = phoneResource{"outbound_number", "+13502231050"}
	adminData(t, f.call(t, "grants_upsert", args))
	calls := len(platform.integrationCalls)
	platform.failCarrier = true
	replay := f.call(t, "grants_upsert", args)
	adminData(t, replay)
	if replay["replayed"] != true || len(platform.integrationCalls) != calls {
		t.Fatal("receipt replay contacted the carrier")
	}
	platform.failCarrier = false
	p, err := f.app.phonePrincipal("project-a", phoneTestIdentity("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if dest, err := p.outboundDestination("", "+13502231050"); err != nil || dest != "desk-alice" || !p.Numbers["+13502231050"] {
		t.Fatal("outbound grants unavailable", dest, err)
	}
	if _, err = p.outboundDestination("not-granted", "+13502231050"); err == nil {
		t.Fatal("foreign outbound destination allowed")
	}
	policy, err := f.app.phonePolicy("project-a")
	if err != nil {
		t.Fatal(err)
	}
	policy.Delegations[0].OutboundNumbers = append(policy.Delegations[0].OutboundNumbers, "+13502239999")
	if w := phoneTestRequest(f.app, nil, "PUT", "/access/policy", policy); w.Code != 200 {
		t.Fatal(w.Body)
	}
	args["id"], args["idempotency_key"], args["resource"] = "unowned", "unowned-number", phoneResource{"outbound_number", "+13502239999"}
	adminCode(t, f.call(t, "grants_upsert", args), "invalid_reference")
	// Number-only permission never grants use of somebody else's browser resource.
	var numberOnly phonePolicy
	numberOnly.resourceGrants = []phoneResourceGrant{{Principal: phoneTestIdentity("alice"), Resource: phoneResource{"outbound_number", "+13502231050"}, Enabled: true, Actions: []string{"call.dial"}}}
	p, err = phonePrincipalFromPolicy("project-a", phoneTestIdentity("alice"), numberOnly)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.outboundDestination("", "+13502231050"); err == nil {
		t.Fatal("number-only grant enabled outbound calling")
	}
}

func TestPhoneResourceAdminUpgradeAndReopen(t *testing.T) {
	f := newPhoneAdminFixture(t)
	legacy := phoneTestCall(t, f.app, "historical-owner", "completed")
	identity := phoneTestIdentity("legacy-user")
	if _, err := f.ctx.AppDB().Exec(`INSERT INTO telephony_call_owners(call_id,project_id,principal,destination_id) VALUES(?,?,?,?)`, legacy.ID, legacy.ProjectID, identity.key(), "historical-desk"); err != nil {
		t.Fatal(err)
	}
	// Apply the new migration to a database with the complete pre-extension schema.
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if filepath.Base(file) == "074_resource_administration.sql" {
			continue
		}
		applyMigrationFile(t, db, file)
	}
	if _, err = db.Exec(`INSERT INTO routing_destinations(id,project_id,name,kind,config_json,created_at,updated_at) VALUES('legacy-desk','project-a','Legacy','browser','{}','','')`); err != nil {
		t.Fatal(err)
	}
	applyMigrationFile(t, db, "migrations/074_resource_administration.sql")
	var name, revision string
	if err = db.QueryRow(`SELECT name,revision FROM routing_destinations WHERE id='legacy-desk'`).Scan(&name, &revision); err != nil || name != "Legacy" || revision == "" {
		t.Fatal("migration changed legacy destination", err)
	}
	grant := f.provision(t, "desk-alice", "alice", "call.read", "call.attach")
	snapshot := filepath.Join(t.TempDir(), "reopen.db")
	if _, err = f.ctx.AppDB().Exec(`VACUUM INTO ?`, snapshot); err != nil {
		t.Fatal(err)
	}
	reopened, err := sql.Open("sqlite", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.SetMaxOpenConns(1)
	f.ctx = sdk.NewAppCtxForTest(f.ctx.Manifest(), reopened, f.ctx.Config(), f.ctx.PlatformAPI(), f.ctx.Logger()).WithProject("project-a")
	globalCtx = f.ctx
	found := adminData(t, f.call(t, "grants_get", map[string]any{"id": grant["id"]}))
	if found["revision"] != grant["revision"] {
		t.Fatal("grant revision changed across reopen")
	}
	result := f.call(t, "destinations_upsert", adminDestinationArgs("desk-alice", "alice"))
	adminData(t, result)
	if result["replayed"] != true {
		t.Fatal("receipt lost across reopen")
	}
	var owner, status string
	if err = reopened.QueryRow(`SELECT o.principal,c.status FROM telephony_call_owners o JOIN calls c ON c.id=o.call_id WHERE c.id=?`, legacy.ID).Scan(&owner, &status); err != nil || owner != identity.key() || status != "completed" {
		t.Fatal("historical call or owner changed", err)
	}
}

func TestPhoneResourceAdminListScopeAndRedaction(t *testing.T) {
	f := newPhoneAdminFixture(t)
	args := adminDestinationArgs("desk-alice", "alice")
	args["config"].(map[string]any)["password"] = "never-return-this"
	adminData(t, f.call(t, "destinations_upsert", args))
	page := adminData(t, f.call(t, "destinations_list", map[string]any{"kind": "browser", "enabled": true, "limit": 1}))
	if len(page["items"].([]any)) != 1 || strings.Contains(mustAdminJSON(page), "never-return-this") {
		t.Fatal("destination list leaked credentials or ignored scope")
	}
	for _, limit := range []any{0, 101, 1.5, "100"} {
		adminCode(t, f.call(t, "grants_list", map[string]any{"limit": limit}), "invalid_argument")
	}
	adminCode(t, f.call(t, "grants_list", map[string]any{"resource": phoneResource{"destination", "foreign"}}), "forbidden")
	adminCode(t, f.call(t, "grants_get", map[string]any{"id": strings.Repeat("x", 129)}), "invalid_argument")
	mutation := adminDestinationArgs("desk-wrong-fields", "alice")
	mutation["project_id"] = "other"
	adminCode(t, f.call(t, "destinations_upsert", mutation), "invalid_argument")
}
