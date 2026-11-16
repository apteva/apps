package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// fakeSecurityTenant mimics the apteva-server endpoints Fleet's rotation
// and containment use: /api/auth/me, /api/auth/keys (list/create/delete)
// and the security API.
type fakeKey struct {
	id      int64
	userID  int64
	raw     string
	prefix  string
	scopes  []string
	revoked bool
}

type fakeSecurityTenant struct {
	mu sync.Mutex
	// containMode: "scope" accepts containment only from a key carrying
	// security.containment (new servers), "403" always refuses an API key,
	// "404" has no security API (old servers).
	containMode string
	// gateScopes mimics servers that only let a key already carrying the
	// containment scope (or a reauthenticated session) grant it.
	gateScopes      bool
	keys            []*fakeKey
	nextID          int64
	minted          int
	failDeleteTimes int
	containCalls    []map[string]any
	srv             *httptest.Server
}

func newRawKey() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "sk-" + hex.EncodeToString(b)
}

func newFakeSecurityTenant(t *testing.T, mode string) (*fakeSecurityTenant, string) {
	t.Helper()
	f := &fakeSecurityTenant{containMode: mode, nextID: 1}
	initial := f.addKey(1, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		k := f.auth(r)
		if k == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": k.userID, "email": "admin@tenant.test"})
	})
	mux.HandleFunc("/api/auth/keys", func(w http.ResponseWriter, r *http.Request) {
		k := f.auth(r)
		if k == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			f.mu.Lock()
			var out []map[string]any
			for _, x := range f.keys {
				if x.userID == k.userID && !x.revoked {
					out = append(out, map[string]any{"id": x.id, "name": "k", "key_prefix": x.prefix})
				}
			}
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(out)
		case http.MethodPost:
			var body struct {
				Scopes []string `json:"scopes"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if f.gateScopes && len(body.Scopes) > 0 {
				callerScoped := false
				for _, s := range k.scopes {
					callerScoped = callerScoped || s == controlKeyScope
				}
				if !callerScoped {
					w.WriteHeader(http.StatusForbidden)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": "interactive_session_required", "required_scope": controlKeyScope})
					return
				}
			}
			nk := f.addKey(k.userID, body.Scopes)
			f.mu.Lock()
			f.minted++
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"id": nk.id, "key": nk.raw, "prefix": nk.prefix})
		}
	})
	mux.HandleFunc("/api/auth/keys/", func(w http.ResponseWriter, r *http.Request) {
		k := f.auth(r)
		if k == nil || r.Method != http.MethodDelete {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failDeleteTimes > 0 {
			f.failDeleteTimes--
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/auth/keys/"), 10, 64)
		for _, x := range f.keys {
			if x.id == id && x.userID == k.userID {
				x.revoked = true
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	})
	mux.HandleFunc("/api/security/", func(w http.ResponseWriter, r *http.Request) {
		k := f.auth(r)
		if k == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if f.containMode == "404" {
			http.NotFound(w, r)
			return
		}
		switch strings.TrimPrefix(r.URL.Path, "/api/security/") {
		case "overview":
			_ = json.NewEncoder(w).Encode(map[string]any{"health": "ok"})
		case "containment/revoke":
			scoped := false
			for _, s := range k.scopes {
				scoped = scoped || s == controlKeyScope
			}
			if f.containMode == "403" || !scoped {
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "interactive_session_required"})
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.containCalls = append(f.containCalls, body)
			keep := map[int64]bool{}
			if ids, ok := body["keep_key_ids"].([]any); ok {
				for _, v := range ids {
					keep[int64(v.(float64))] = true
				}
			}
			revoked := 0
			for _, x := range f.keys {
				if !x.revoked && !keep[x.id] {
					x.revoked = true
					revoked++
				}
			}
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"complete": true, "counts": map[string]int{"revoked": revoked}})
		default:
			http.NotFound(w, r)
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f, initial.raw
}

func (f *fakeSecurityTenant) addKey(userID int64, scopes []string) *fakeKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw := newRawKey()
	k := &fakeKey{id: f.nextID, userID: userID, raw: raw, prefix: raw[:11], scopes: scopes}
	f.nextID++
	f.keys = append(f.keys, k)
	return k
}

func (f *fakeSecurityTenant) auth(r *http.Request) *fakeKey {
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.keys {
		if k.raw == raw && !k.revoked {
			return k
		}
	}
	return nil
}

func (f *fakeSecurityTenant) active(raw string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.keys {
		if k.raw == raw {
			return !k.revoked
		}
	}
	return false
}

// seedRemoteTenant registers a remote tenant pointing at the fake server.
func seedRemoteTenant(t *testing.T, app *App, slug, baseURL, key string) string {
	t.Helper()
	enc, err := app.keys.seal([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	tn := &Tenant{Slug: slug, Kind: KindRemote, BaseURL: baseURL, OwnerEmail: slug + "@example.com", Status: StatusActive, CurrentVersion: "0.100.13"}
	if err := app.store.insert(tn, enc, nil); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	if err := app.store.attachAPIKey(tn.ID, enc); err != nil {
		t.Fatalf("attach: %v", err)
	}
	return tn.ID
}

func storedKey(t *testing.T, app *App, id string) string {
	t.Helper()
	_, enc, err := app.store.get(id)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := app.keys.open(enc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func lastEvent(t *testing.T, app *App, id, kind string) Event {
	t.Helper()
	events, err := app.store.recentEvents(id, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("no %s event", kind)
	return Event{}
}

func TestRotateControlKey_RotatesAndRevokesOld(t *testing.T) {
	app, ctx := newTestApp(t)
	f, oldKey := newFakeSecurityTenant(t, "scope")
	id := seedRemoteTenant(t, app, "acme", f.srv.URL, oldKey)

	out, err := app.toolRotateControlKey(ctx, withActor(map[string]any{"tenant_id": id}, "agent:7/thread:t1"))
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	res := out.(*keyRotationResult)
	newKey := storedKey(t, app, id)
	if newKey == oldKey || res.Status != "rotated" || !res.OldKeyRevoked || res.OldKeyRejected == nil || !*res.OldKeyRejected {
		t.Fatalf("unexpected result %+v", res)
	}
	if f.active(oldKey) || !f.active(newKey) {
		t.Fatal("old key must be revoked and new key active on the tenant")
	}
	st, _ := app.store.controlKeyState(id)
	if st.KeyID != res.NewKeyID || st.KeyPrefix != newKey[:11] || st.RevokePrefix != "" || len(st.PendingEnc) != 0 {
		t.Fatalf("stored state %+v", st)
	}
	if ev := lastEvent(t, app, id, "control_key_rotated"); ev.Actor != "agent:7/thread:t1" {
		t.Fatalf("event actor = %q", ev.Actor)
	}
}

func TestRotateControlKey_ResumesAfterRevokeFailure(t *testing.T) {
	app, ctx := newTestApp(t)
	f, oldKey := newFakeSecurityTenant(t, "scope")
	f.failDeleteTimes = 1
	id := seedRemoteTenant(t, app, "acme", f.srv.URL, oldKey)

	if _, err := app.toolRotateControlKey(ctx, map[string]any{"tenant_id": id}); err == nil {
		t.Fatal("first rotation should report the failed revoke")
	}
	newKey := storedKey(t, app, id)
	if newKey == oldKey {
		t.Fatal("Fleet must already hold the new key after the swap")
	}
	if st, _ := app.store.controlKeyState(id); st.RevokePrefix != oldKey[:11] {
		t.Fatalf("revoke prefix = %q", st.RevokePrefix)
	}
	out, err := app.toolRotateControlKey(ctx, map[string]any{"tenant_id": id})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	res := out.(*keyRotationResult)
	if res.Status != "resumed" || !res.OldKeyRevoked || f.active(oldKey) || storedKey(t, app, id) != newKey {
		t.Fatalf("resume result %+v", res)
	}
	if f.minted != 1 {
		t.Fatalf("resume must not mint another key; minted=%d", f.minted)
	}
}

func TestRotateControlKey_AdoptsKeyFromCrashedRun(t *testing.T) {
	app, ctx := newTestApp(t)
	f, oldKey := newFakeSecurityTenant(t, "scope")
	id := seedRemoteTenant(t, app, "acme", f.srv.URL, oldKey)
	// Simulate a crash right after minting: the key exists on the tenant
	// and is sealed as pending, but was never swapped in.
	orphan := f.addKey(1, []string{controlKeyScope})
	sealed, _ := app.keys.seal([]byte(orphan.raw))
	if err := app.store.savePendingControlKey(id, sealed, orphan.id, orphan.prefix); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolRotateControlKey(ctx, map[string]any{"tenant_id": id})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	res := out.(*keyRotationResult)
	if res.Status != "resumed" || storedKey(t, app, id) != orphan.raw || f.active(oldKey) || f.minted != 0 {
		t.Fatalf("adopt result %+v minted=%d", res, f.minted)
	}
}

func TestRotateControlKey_RejectedKeyFailsWithoutChanges(t *testing.T) {
	app, ctx := newTestApp(t)
	f, _ := newFakeSecurityTenant(t, "scope")
	id := seedRemoteTenant(t, app, "acme", f.srv.URL, "sk-0000000000000000dead")
	if _, err := app.toolRotateControlKey(ctx, map[string]any{"tenant_id": id}); err == nil || !strings.Contains(err.Error(), "tenant_attach_key") {
		t.Fatalf("err = %v", err)
	}
	if storedKey(t, app, id) != "sk-0000000000000000dead" {
		t.Fatal("a failed rotation must not replace the stored key")
	}
}

func TestTenantContain_Outcomes(t *testing.T) {
	cases := []struct{ mode, want string }{
		{"scope", "confirmed"},
		{"403", "partial"},
		{"404", "unsupported"},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			app, ctx := newTestApp(t)
			f, oldKey := newFakeSecurityTenant(t, c.mode)
			extra := f.addKey(1, nil) // e.g. a key an attacker created
			id := seedRemoteTenant(t, app, "acme", f.srv.URL, oldKey)
			out, err := app.toolTenantContain(ctx, map[string]any{"tenant_id": id, "reason": "incident 42"})
			if err != nil {
				t.Fatal(err)
			}
			res := out.(*tenantContainResult)
			if res.Status != c.want {
				t.Fatalf("status=%s want %s: %+v", res.Status, c.want, res)
			}
			if f.active(oldKey) {
				t.Fatal("every outcome must have rotated the control key")
			}
			newKey := storedKey(t, app, id)
			if !f.active(newKey) {
				t.Fatal("containment must keep Fleet's new control key")
			}
			if c.mode == "scope" {
				if f.active(extra.raw) {
					t.Fatal("confirmed containment must revoke other keys")
				}
				if len(f.containCalls) != 1 || f.containCalls[0]["reason"] != "fleet containment: incident 42" {
					t.Fatalf("contain calls %+v", f.containCalls)
				}
			}
		})
	}
}

func TestTenantContainAll_CompleteOnlyWhenEveryTenantConfirmed(t *testing.T) {
	app, ctx := newTestApp(t)
	good, gk := newFakeSecurityTenant(t, "scope")
	old, ok := newFakeSecurityTenant(t, "404")
	seedRemoteTenant(t, app, "good", good.srv.URL, gk)
	seedRemoteTenant(t, app, "old", old.srv.URL, ok)
	out, err := app.toolTenantContainAll(ctx, map[string]any{"reason": "incident"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["complete"] != false {
		t.Fatalf("complete must be false with an unsupported tenant: %+v", m)
	}
	counts := m["counts"].(map[string]int)
	if counts["confirmed"] != 1 || counts["unsupported"] != 1 {
		t.Fatalf("counts %+v", counts)
	}
}

func TestTenantSecurityOverview_UnsupportedReportsVersion(t *testing.T) {
	app, ctx := newTestApp(t)
	f, key := newFakeSecurityTenant(t, "404")
	id := seedRemoteTenant(t, app, "acme", f.srv.URL, key)
	out, err := app.toolTenantSecurityOverview(ctx, map[string]any{"tenant_id": id})
	if err != nil {
		t.Fatalf("an old tenant must not be an error: %v", err)
	}
	m := out.(map[string]any)
	if m["status"] != "unsupported" || m["version"] != "0.100.13" {
		t.Fatalf("got %+v", m)
	}
}

func TestRevealAPIKey_DisabledByDefault(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "acme", StatusActive)
	if _, err := app.toolRevealAPIKey(ctx, map[string]any{"tenant_id": id}); err == nil || !strings.Contains(err.Error(), revealAPIKeyConfig) {
		t.Fatalf("reveal must be disabled by default, err=%v", err)
	}
	lastEvent(t, app, id, "api_key_reveal_denied")
}

func TestToolCallsRecordRealCaller(t *testing.T) {
	app, _ := newTestApp(t, tk.WithConfig(map[string]string{revealAPIKeyConfig: "true"}))
	id := seedTenant(t, app, "acme", StatusActive)
	var reveal sdk.Tool
	for _, tool := range app.MCPTools() {
		if tool.Name == "tenant_reveal_api_key" {
			reveal = tool
		}
	}
	if reveal.HandlerCtx == nil {
		t.Fatal("tools must be wrapped to receive the caller")
	}
	ctx := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 7, ThreadID: "t1"})
	// A model-supplied actor must be overwritten.
	if _, err := reveal.HandlerCtx(ctx, globalCtx, map[string]any{"tenant_id": id, actorArg: "user:1 admin@spoof"}); err != nil {
		t.Fatal(err)
	}
	if ev := lastEvent(t, app, id, "api_key_revealed"); ev.Actor != "agent:7/thread:t1" {
		t.Fatalf("actor=%q", ev.Actor)
	}
}

func TestCallerActorFormats(t *testing.T) {
	cases := map[string]*sdk.Caller{
		"unknown":                 nil,
		"app:backup#12":           {AppName: "backup", AppInstallID: 12},
		"subject:user:9 a@b.test": {SubjectType: "user", SubjectID: "9", SubjectEmail: "a@b.test"},
		"agent:3":                 {AgentID: 3},
		"agent:3/thread:conv-1":   {AgentID: 3, ThreadID: "conv-1"},
		"platform":                {},
	}
	for want, c := range cases {
		if got := callerActor(c); got != want {
			t.Errorf("callerActor(%+v)=%q want %q", c, got, want)
		}
	}
}

func TestIncidentReport_ListsSensitiveActionsWithCallers(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "acme", StatusActive)
	_ = app.store.recordEvent(id, "api_key_revealed", "agent:7/thread:t1", nil)
	_ = app.store.recordEvent(id, "platform_call", "agent:7", map[string]any{"resource": "users", "action": "create"})
	_ = app.store.recordEvent(id, "platform_call", "agent:7", map[string]any{"resource": "users", "action": "list"})
	_ = app.store.recordEvent(id, "remote_call", "user", map[string]any{"app": "x"})
	_ = app.store.recordEvent(id, "health_failed", "fleet", nil)

	out, err := app.toolIncidentReport(ctx, map[string]any{"since": "1h"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["total"] != 3 {
		t.Fatalf("total=%v want 3 (reveal, user create, remote run): %+v", m["total"], m)
	}
	byActor := m["by_actor"].(map[string]int)
	if byActor["agent:7/thread:t1"] != 1 || byActor["agent:7"] != 1 || byActor["user"] != 1 {
		t.Fatalf("by_actor %+v", byActor)
	}
	notes := strings.Join(m["notes"].([]string), " ")
	if !strings.Contains(notes, "legacy actor") {
		t.Fatalf("legacy actor note missing: %s", notes)
	}
}

func (f *fakeSecurityTenant) scopesOf(raw string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.keys {
		if k.raw == raw {
			return k.scopes
		}
	}
	return nil
}

// On a server that gates the scope, an unscoped control key still rotates
// (unscoped) and containment explains how to get a scoped key.
func TestRotateControlKey_GatedTenantFallsBackToUnscopedKey(t *testing.T) {
	app, ctx := newTestApp(t)
	f, oldKey := newFakeSecurityTenant(t, "scope")
	f.gateScopes = true
	id := seedRemoteTenant(t, app, "acme", f.srv.URL, oldKey)

	out, err := app.toolRotateControlKey(ctx, map[string]any{"tenant_id": id})
	if err != nil {
		t.Fatalf("rotation must still work with an unscoped key: %v", err)
	}
	res := out.(*keyRotationResult)
	if res.ContainScope == nil || *res.ContainScope || f.active(oldKey) {
		t.Fatalf("result %+v", res)
	}
	if len(f.scopesOf(storedKey(t, app, id))) != 0 {
		t.Fatal("fallback key should be unscoped")
	}

	c, err := app.toolTenantContain(ctx, map[string]any{"tenant_id": id, "reason": "incident"})
	if err != nil {
		t.Fatal(err)
	}
	cr := c.(*tenantContainResult)
	if cr.Status != "partial" || len(cr.Targets) != 1 || !strings.Contains(cr.Targets[0].Detail, "tenant_attach_key") {
		t.Fatalf("containment should be partial with the attach hint: %+v", cr)
	}
}

// Once the tenant has a scoped key, rotation keeps the scope on a gated server.
func TestRotateControlKey_GatedTenantKeepsScope(t *testing.T) {
	app, ctx := newTestApp(t)
	f, _ := newFakeSecurityTenant(t, "scope")
	f.gateScopes = true
	scoped := f.addKey(1, []string{controlKeyScope})
	id := seedRemoteTenant(t, app, "acme", f.srv.URL, scoped.raw)

	c, err := app.toolTenantContain(ctx, map[string]any{"tenant_id": id, "reason": "incident"})
	if err != nil {
		t.Fatal(err)
	}
	if cr := c.(*tenantContainResult); cr.Status != "confirmed" {
		t.Fatalf("scoped tenant should confirm: %+v", cr)
	}
	if got := f.scopesOf(storedKey(t, app, id)); len(got) != 1 || got[0] != controlKeyScope {
		t.Fatalf("successor scopes %v", got)
	}
}
