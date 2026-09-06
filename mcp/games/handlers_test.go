package main

// Tier 1 — every /v1 handler, the admin routes, and the MCP tools against
// an in-memory SQLite, with the Auth and Analytics apps replaced by an
// in-process fake that signs real EdDSA tokens. Tier 3 (real agent) lives
// in scenarios/*.yaml.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// fakeAuth stands in for the Auth (and Analytics) sidecars behind
// ctx.PlatformAPI(). It keeps identity → user mappings, disables users,
// and signs JWTs with a key it also serves through auth_jwks_get.
type fakeAuth struct {
	tk.BasePlatformClient
	mu             sync.Mutex
	priv           ed25519.PrivateKey
	pub            ed25519.PublicKey
	kid            string
	users          map[string]int64
	kinds          map[int64]string
	names          map[int64]string
	disabled       map[int64]bool
	disableReasons map[int64]string
	failTool       string
	nextID         int64
	clients        map[string][]map[string]any
	orgs           map[string]bool
	calls          []string
	analytics      []map[string]any
}

func newFakeAuth(t *testing.T) *fakeAuth {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return &fakeAuth{
		priv: priv, pub: pub, kid: "kid-test",
		users: map[string]int64{}, kinds: map[int64]string{}, names: map[int64]string{}, disabled: map[int64]bool{}, disableReasons: map[int64]string{},
		nextID: 100, clients: map[string][]map[string]any{}, orgs: map[string]bool{"default": true},
	}
}

func signTestToken(priv ed25519.PrivateKey, kid string, uid int64, kind, org string, exp time.Time) string {
	h, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": kid})
	c, _ := json.Marshal(map[string]any{
		"sub": strconv.FormatInt(uid, 10), "org": org, "kind": kind,
		"iat": time.Now().Unix(), "exp": exp.Unix(), "iss": "http://test/orgs/" + org, "aud": "akc_test", "azp": "akc_test",
	})
	enc := base64.RawURLEncoding.EncodeToString
	signing := enc(h) + "." + enc(c)
	return signing + "." + enc(ed25519.Sign(priv, []byte(signing)))
}

func argInt(in map[string]any, key string) int64 {
	switch v := in[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}

func (f *fakeAuth) callCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

func (f *fakeAuth) CallAppResult(app, tool string, in map[string]any, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, app+"."+tool)
	if f.failTool == app+"."+tool {
		return fmt.Errorf("forced dependency outage")
	}
	identityKey := func() string {
		p, _ := in["provider"].(string)
		s, _ := in["provider_user_id"].(string)
		org, _ := in["organization_slug"].(string)
		if org == "default" {
			return p + "|" + s
		}
		return org + "|" + p + "|" + s
	}
	org, _ := in["organization_slug"].(string)
	var res any
	switch app + "." + tool {
	case "auth.auth_orgs_list":
		orgs := []map[string]any{}
		for slug := range f.orgs {
			orgs = append(orgs, map[string]any{"slug": slug})
		}
		res = map[string]any{"organizations": orgs}
	case "auth.auth_orgs_create":
		slug, _ := in["slug"].(string)
		f.orgs[slug] = true
		res = map[string]any{"organization": map[string]any{"slug": slug}}
	case "auth.auth_clients_list":
		res = map[string]any{"clients": f.clients[org]}
	case "auth.auth_clients_create":
		id := "akc_test"
		if org != "default" {
			id = "akc_" + org
		}
		client := map[string]any{"client_id": id, "name": in["name"], "status": "active"}
		f.clients[org] = append(f.clients[org], client)
		res = client
	case "auth.auth_public_login_identity":
		key := identityKey()
		uid, ok := f.users[key]
		created := false
		if !ok {
			if v, has := in["create_if_missing"].(bool); has && !v {
				return fmt.Errorf("identity_not_found")
			}
			f.nextID++
			uid = f.nextID
			f.users[key] = uid
			f.kinds[uid] = "guest"
			if n, _ := in["display_name"].(string); n != "" {
				f.names[uid] = n
			}
			created = true
		}
		if f.disabled[uid] {
			return fmt.Errorf("user_inactive")
		}
		token := signTestToken(f.priv, f.kid, uid, f.kinds[uid], org, time.Now().Add(15*time.Minute))
		if org != "default" {
			token = resignClaims(f.priv, token, map[string]any{"aud": in["client_id"], "azp": in["client_id"]})
		}
		res = map[string]any{
			"user":          map[string]any{"id": uid, "kind": f.kinds[uid], "display_name": f.names[uid]},
			"created":       created,
			"access_token":  token,
			"refresh_token": fmt.Sprintf("refresh-%d", uid),
			"expires_in":    900, "token_type": "Bearer", "client_id": in["client_id"], "organization_slug": org,
		}
	case "auth.auth_jwks_get":
		res = map[string]any{
			"organization_slug": org, "issuer": "http://test/orgs/" + org,
			"keys": []map[string]any{{
				"kid": f.kid, "kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "use": "sig",
				"x": base64.RawURLEncoding.EncodeToString(f.pub),
			}},
		}
	case "auth.auth_identities_link":
		key := identityKey()
		uid := argInt(in, "user_id")
		if other, ok := f.users[key]; ok && other != uid {
			return fmt.Errorf("identity_already_linked: this provider subject belongs to another user")
		}
		f.users[key] = uid
		res = map[string]any{"linked": true}
	case "auth.auth_identities_list":
		ids := []map[string]any{}
		if uid, ok := f.users[identityKey()]; ok {
			ids = append(ids, map[string]any{"user_id": uid})
		}
		res = map[string]any{"identities": ids, "count": len(ids)}
	case "auth.auth_users_disable":
		f.disabled[argInt(in, "user_id")] = true
		f.disableReasons[argInt(in, "user_id")], _ = in["reason"].(string)
		res = map[string]any{"ok": true}
	case "auth.auth_audit_search":
		metadata, _ := json.Marshal(map[string]any{"reason": f.disableReasons[argInt(in, "user_id")]})
		res = map[string]any{"events": []map[string]any{{"metadata": string(metadata)}}}
	case "auth.auth_users_enable":
		f.disabled[argInt(in, "user_id")] = false
		res = map[string]any{"ok": true}
	case "auth.auth_users_revoke_sessions":
		res = map[string]any{"revoked_count": 1}
	case "analytics.analytics_track":
		f.analytics = append(f.analytics, in)
		res = map[string]any{"ok": true}
	default:
		return fmt.Errorf("unexpected app call %s.%s", app, tool)
	}
	b, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

type fixture struct {
	ctx    *sdk.AppCtx
	auth   *fakeAuth
	events *tk.EmitRecorder
	app    *App
	pid    GameScope
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	fake := newFakeAuth(t)
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithPlatform(fake),
		tk.WithEmitter(rec),
		tk.WithConfig(map[string]string{"analytics_enabled": "false"}))
	if err := initializeGames(ctx); err != nil {
		t.Fatal(err)
	}
	resetJWKSCache()
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = nil; resetJWKSCache() })
	return &fixture{ctx: ctx, auth: fake, events: rec, app: &App{}, pid: GameScope{"test-proj", "legacy-test-proj"}}
}

func doReq(h http.HandlerFunc, method, path string, body any, pathValues map[string]string, headers ...string) *httptest.ResponseRecorder {
	var rdr io.Reader = bytes.NewReader(nil)
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rdr)
	r.Header.Set("Content-Type", "application/json")
	for k, v := range pathValues {
		r.SetPathValue(k, v)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func bearer(tok string) []string { return []string{"Authorization", "Bearer " + tok} }

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(strings.NewReader(rec.Body.String())).Decode(&out); err != nil {
		t.Fatalf("decode body=%s: %v", rec.Body.String(), err)
	}
	return out
}

func (f *fixture) loginDevice(t *testing.T, deviceID, name string) (map[string]any, string) {
	t.Helper()
	rec := doReq(f.app.handleLoginDevice, "POST", "/v1/login/device",
		map[string]any{"device_id": deviceID, "display_name": name}, nil)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("login status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decode(t, rec)
	tok, _ := out["access_token"].(string)
	if tok == "" {
		t.Fatalf("login returned no access token: %v", out)
	}
	return out, tok
}

func playerID(t *testing.T, out map[string]any) int64 {
	t.Helper()
	p, _ := out["player"].(map[string]any)
	id, _ := p["id"].(float64)
	if id == 0 {
		t.Fatalf("no player id in %v", out)
	}
	return int64(id)
}

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

// ─── login + tokens ──────────────────────────────────────────────────

func TestLoginDevice_CreatesPlayerThenReturns(t *testing.T) {
	f := newFixture(t)
	first, tok := f.loginDevice(t, "ios-abc", "Ada")
	if created, _ := first["created"].(bool); !created {
		t.Fatal("first login must create the player")
	}
	p := first["player"].(map[string]any)
	if p["display_name"] != "Ada" || p["kind"] != "guest" || p["status"] != "active" {
		t.Errorf("player = %v", p)
	}
	if auth := first["auth"].(map[string]any); auth["client_id"] != "akc_test" || auth["refresh_path"] != "/api/apps/auth/refresh" {
		t.Errorf("auth block = %v", auth)
	}
	rec := doReq(f.app.handleMe, "GET", "/v1/me", nil, nil, bearer(tok)...)
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/me status=%d body=%s", rec.Code, rec.Body.String())
	}
	if me := decode(t, rec)["player"].(map[string]any); me["id"] != p["id"] {
		t.Errorf("/v1/me returned %v", me)
	}

	second, _ := f.loginDevice(t, "ios-abc", "")
	if created, _ := second["created"].(bool); created {
		t.Fatal("second login must reuse the player")
	}
	if playerID(t, second) != playerID(t, first) {
		t.Fatal("second login resolved a different player")
	}
	if n, _ := second["player"].(map[string]any)["login_count"].(float64); n != 2 {
		t.Errorf("login_count = %v, want 2", n)
	}
	if n := f.auth.callCount("auth.auth_clients_create"); n != 1 {
		t.Errorf("auth client registered %d times, want once", n)
	}
	if n := len(f.delivered(t).EventsByTopic("player.created")); n != 1 {
		t.Errorf("player.created emitted %d times", n)
	}
}

func TestLogin_DefaultDisplayNameAndCustomID(t *testing.T) {
	f := newFixture(t)
	out, _ := f.loginDevice(t, "dev-2", "")
	if name, _ := out["player"].(map[string]any)["display_name"].(string); !strings.HasPrefix(name, "Player ") {
		t.Errorf("default display name = %q", name)
	}
	rec := doReq(f.app.handleLoginCustom, "POST", "/v1/login/custom", map[string]any{"custom_id": "acct-1"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("custom login status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = doReq(f.app.handleLoginCustom, "POST", "/v1/login/custom", map[string]any{"custom_id": ""}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty custom_id status=%d", rec.Code)
	}
}

func TestLogin_RejectsBadTokens(t *testing.T) {
	f := newFixture(t)
	if rec := doReq(f.app.handleLoginDevice, "POST", "/v1/login/device", map[string]any{"device_id": ""}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("empty device_id status=%d", rec.Code)
	}
	if rec := doReq(f.app.handleMe, "GET", "/v1/me", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no bearer status=%d", rec.Code)
	}
	if rec := doReq(f.app.handleMe, "GET", "/v1/me", nil, nil, bearer("garbage")...); rec.Code != http.StatusUnauthorized {
		t.Errorf("garbage token status=%d", rec.Code)
	}
	f.loginDevice(t, "dev-3", "")
	uid := f.auth.users["device|"+identitySubject("dev-3")]
	future := time.Now().Add(time.Hour)

	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	forged := signTestToken(otherPriv, f.auth.kid, uid, "guest", "default", future)
	if rec := doReq(f.app.handleMe, "GET", "/v1/me", nil, nil, bearer(forged)...); rec.Code != http.StatusUnauthorized {
		t.Errorf("forged token status=%d body=%s", rec.Code, rec.Body.String())
	}
	expired := signTestToken(f.auth.priv, f.auth.kid, uid, "guest", "default", time.Now().Add(-time.Minute))
	if rec := doReq(f.app.handleMe, "GET", "/v1/me", nil, nil, bearer(expired)...); rec.Code != http.StatusUnauthorized {
		t.Errorf("expired token status=%d", rec.Code)
	}
	unknownKid := signTestToken(f.auth.priv, "kid-other", uid, "guest", "default", future)
	if rec := doReq(f.app.handleMe, "GET", "/v1/me", nil, nil, bearer(unknownKid)...); rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown kid status=%d", rec.Code)
	}
	orphan := signTestToken(f.auth.priv, f.auth.kid, 4242, "guest", "default", future)
	rec := doReq(f.app.handleMe, "GET", "/v1/me", nil, nil, bearer(orphan)...)
	if rec.Code != http.StatusUnauthorized || decode(t, rec)["error"] != "player_not_found" {
		t.Errorf("orphan token status=%d body=%s", rec.Code, rec.Body.String())
	}
	otherOrg := signTestToken(f.auth.priv, f.auth.kid, uid, "guest", "other-org", future)
	if rec := doReq(f.app.handleMe, "GET", "/v1/me", nil, nil, bearer(otherOrg)...); rec.Code != http.StatusUnauthorized {
		t.Errorf("other-org token status=%d", rec.Code)
	}
}

func TestLoginLink_DeviceResolvesSamePlayer(t *testing.T) {
	f := newFixture(t)
	rec := doReq(f.app.handleLoginCustom, "POST", "/v1/login/custom", map[string]any{"custom_id": "acct-9", "display_name": "Bo"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("custom login status=%d body=%s", rec.Code, rec.Body.String())
	}
	bo := decode(t, rec)
	tok, _ := bo["access_token"].(string)
	boID := playerID(t, bo)

	rec = doReq(f.app.handleLoginLink, "POST", "/v1/login/link", map[string]any{"device_id": "phone-1"}, nil, bearer(tok)...)
	if rec.Code != http.StatusOK {
		t.Fatalf("link status=%d body=%s", rec.Code, rec.Body.String())
	}
	viaDevice, _ := f.loginDevice(t, "phone-1", "")
	if created, _ := viaDevice["created"].(bool); created || playerID(t, viaDevice) != boID {
		t.Fatalf("device login after link = %v", viaDevice)
	}

	f.loginDevice(t, "phone-2", "Cy") // phone-2 now belongs to someone else
	rec = doReq(f.app.handleLoginLink, "POST", "/v1/login/link", map[string]any{"device_id": "phone-2"}, nil, bearer(tok)...)
	if rec.Code != http.StatusConflict {
		t.Errorf("linking another player's device status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = doReq(f.app.handleLoginLink, "POST", "/v1/login/link", map[string]any{"provider": "steam", "provider_user_id": "1"}, nil, bearer(tok)...)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unsupported provider status=%d", rec.Code)
	}
	if n := len(f.delivered(t).EventsByTopic("player.linked")); n != 1 {
		t.Errorf("player.linked emitted %d times", n)
	}
}

// ─── player data ─────────────────────────────────────────────────────

func TestData_VersioningAndVisibility(t *testing.T) {
	f := newFixture(t)
	out, tok := f.loginDevice(t, "dev-d", "Dee")
	pid := playerID(t, out)
	put := func(key string, body map[string]any) *httptest.ResponseRecorder {
		return doReq(f.app.handleDataPut, "PUT", "/v1/data/"+key, body, map[string]string{"key": key}, bearer(tok)...)
	}
	rec := put("save", map[string]any{"value": map[string]any{"level": 3}})
	if rec.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", rec.Code, rec.Body.String())
	}
	if v, _ := decode(t, rec)["data"].(map[string]any)["version"].(float64); v != 1 {
		t.Errorf("first version = %v", v)
	}
	rec = put("save", map[string]any{"value": map[string]any{"level": 4}, "version": 1})
	if rec.Code != http.StatusOK {
		t.Fatalf("versioned put status=%d body=%s", rec.Code, rec.Body.String())
	}
	if v, _ := decode(t, rec)["data"].(map[string]any)["version"].(float64); v != 2 {
		t.Errorf("second version = %v", v)
	}
	rec = put("save", map[string]any{"value": map[string]any{"level": 5}, "version": 1})
	if rec.Code != http.StatusConflict || decode(t, rec)["error"] != "version_conflict" {
		t.Errorf("stale put status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doReq(f.app.handleDataPut, "PUT", "/v1/data/bad-key", map[string]any{"value": 1}, map[string]string{"key": "bad key!"}, bearer(tok)...); rec.Code != http.StatusBadRequest {
		t.Errorf("bad key status=%d", rec.Code)
	}
	if rec := put("cfg", map[string]any{"value": 1, "visibility": "server"}); rec.Code != http.StatusForbidden {
		t.Errorf("client writing server key status=%d", rec.Code)
	}
	if rec := put("empty", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Errorf("missing value status=%d", rec.Code)
	}

	if _, err := f.app.toolDataSet(f.ctx, map[string]any{
		"player_id": pid, "key": "flags", "value": map[string]any{"cheater": false}, "visibility": "server",
	}); err != nil {
		t.Fatalf("tool data_set: %v", err)
	}
	rec = doReq(f.app.handleDataGet, "GET", "/v1/data/flags", nil, map[string]string{"key": "flags"}, bearer(tok)...)
	if rec.Code != http.StatusNotFound {
		t.Errorf("server key must be invisible to clients, status=%d", rec.Code)
	}
	if rec := put("flags", map[string]any{"value": 1}); rec.Code != http.StatusForbidden {
		t.Errorf("client overwriting server key status=%d", rec.Code)
	}
	rec = doReq(f.app.handleDataList, "GET", "/v1/data", nil, nil, bearer(tok)...)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d", rec.Code)
	}
	if entries := decode(t, rec)["data"].([]any); len(entries) != 1 || entries[0].(map[string]any)["key"] != "save" {
		t.Errorf("client list = %v", entries)
	}
	toolOut, err := f.app.toolDataGet(f.ctx, map[string]any{"player_id": pid})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := toolOut.(map[string]any)["count"].(int); n != 2 {
		t.Errorf("tool sees %d keys, want 2", n)
	}
	if _, err := f.app.toolDataSet(f.ctx, map[string]any{"player_id": pid, "key": "save", "value": map[string]any{}, "version": 1}); err == nil || !strings.Contains(err.Error(), "version_conflict") {
		t.Errorf("stale tool write should conflict, got %v", err)
	}
	rec = doReq(f.app.handleDataDelete, "DELETE", "/v1/data/save", nil, map[string]string{"key": "save"}, bearer(tok)...)
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete status=%d", rec.Code)
	}
	rec = doReq(f.app.handleDataGet, "GET", "/v1/data/save", nil, map[string]string{"key": "save"}, bearer(tok)...)
	if rec.Code != http.StatusNotFound {
		t.Errorf("deleted key status=%d", rec.Code)
	}
}

// ─── statistics ──────────────────────────────────────────────────────

func TestStats_ClientGateAndAggregation(t *testing.T) {
	f := newFixture(t)
	out, tok := f.loginDevice(t, "dev-s", "Sam")
	pid := playerID(t, out)
	if _, err := f.app.toolStatsDefine(f.ctx, map[string]any{"name": "score", "aggregation": "max", "client_writable": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.toolStatsDefine(f.ctx, map[string]any{"name": "xp", "aggregation": "sum"}); err != nil {
		t.Fatal(err)
	}
	post := func(updates ...map[string]any) map[string]any {
		rec := doReq(f.app.handleStatsPost, "POST", "/v1/stats", map[string]any{"updates": updates}, nil, bearer(tok)...)
		if rec.Code != http.StatusOK {
			t.Fatalf("stats post status=%d body=%s", rec.Code, rec.Body.String())
		}
		return decode(t, rec)
	}
	res := post(map[string]any{"stat": "score", "value": 50}, map[string]any{"stat": "xp", "value": 10}, map[string]any{"stat": "nope", "value": 1})
	if applied := res["applied"].([]any); len(applied) != 1 || applied[0] != "score" {
		t.Errorf("applied = %v", applied)
	}
	if rejected := res["rejected"].([]any); len(rejected) != 2 {
		t.Errorf("rejected = %v", rejected)
	}
	statValue := func(res map[string]any, name string) float64 {
		for _, s := range res["stats"].([]any) {
			m := s.(map[string]any)
			if m["stat"] == name {
				return m["value"].(float64)
			}
		}
		return -1
	}
	if v := statValue(post(map[string]any{"stat": "score", "value": 30}), "score"); v != 50 {
		t.Errorf("max stat after lower write = %v, want 50", v)
	}
	if v := statValue(post(map[string]any{"stat": "score", "value": 70}), "score"); v != 70 {
		t.Errorf("max stat after higher write = %v, want 70", v)
	}
	for _, inc := range []float64{10, 15} {
		if _, err := f.app.toolStatsUpdate(f.ctx, map[string]any{"player_id": pid, "stat": "xp", "value": inc}); err != nil {
			t.Fatal(err)
		}
	}
	statsOut, _ := f.app.toolStatsGet(f.ctx, map[string]any{"player_id": pid})
	xp := -1.0
	for _, s := range statsOut.(map[string]any)["stats"].([]PlayerStat) {
		if s.Stat == "xp" {
			xp = s.Value
		}
	}
	if xp != 25 {
		t.Errorf("sum stat = %v, want 25", xp)
	}
	// Undefined stats written by the server are auto-defined as last-value, server-only.
	for _, v := range []float64{3, 2} {
		if _, err := f.app.toolStatsUpdate(f.ctx, map[string]any{"player_id": pid, "updates": []any{map[string]any{"stat": "level", "value": v}}}); err != nil {
			t.Fatal(err)
		}
	}
	def, _ := dbGetStatDef(f.ctx.AppDB(), f.pid, "level")
	if def == nil || def.Aggregation != "last" || def.ClientWritable {
		t.Errorf("auto-defined stat = %+v", def)
	}
	level, _ := dbGetPlayerStat(f.ctx.AppDB(), f.pid, pid, "level")
	if level == nil || level.Value != 2 {
		t.Errorf("last stat = %+v, want 2", level)
	}
	if n := len(f.delivered(t).EventsByTopic("stat.updated")); n < 5 {
		t.Errorf("stat.updated emitted %d times", n)
	}
}

// ─── leaderboards ────────────────────────────────────────────────────

func TestLeaderboards_RanksAroundAndHistory(t *testing.T) {
	f := newFixture(t)
	if _, err := f.app.toolStatsDefine(f.ctx, map[string]any{"name": "score", "aggregation": "max", "client_writable": true}); err != nil {
		t.Fatal(err)
	}
	lbOut, err := f.app.toolLeaderboardsCreate(f.ctx, map[string]any{"name": "Weekly_Score", "stat": "score", "reset": "weekly", "display_name": "Weekly"})
	if err != nil {
		t.Fatal(err)
	}
	lb := lbOut.(map[string]any)["leaderboard"].(*Leaderboard)
	if lb.Name != "weekly_score" || !strings.Contains(lb.CurrentPeriod, "-W") {
		t.Fatalf("leaderboard = %+v", lb)
	}
	ids := []int64{}
	toks := []string{}
	for i, name := range []string{"P1", "P2", "P3"} {
		out, tok := f.loginDevice(t, fmt.Sprintf("dev-%d", i), name)
		ids = append(ids, playerID(t, out))
		toks = append(toks, tok)
	}
	for i, score := range []float64{50, 80, 65} {
		if _, err := f.app.toolStatsUpdate(f.ctx, map[string]any{"player_id": ids[i], "stat": "score", "value": score}); err != nil {
			t.Fatal(err)
		}
	}
	rec := doReq(f.app.handleLeaderboardGet, "GET", "/v1/leaderboards/weekly_score", nil, map[string]string{"name": "weekly_score"}, bearer(toks[0])...)
	if rec.Code != http.StatusOK {
		t.Fatalf("leaderboard status=%d body=%s", rec.Code, rec.Body.String())
	}
	page := decode(t, rec)
	entries := page["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("entries = %v", entries)
	}
	order := []int64{ids[1], ids[2], ids[0]}
	for i, e := range entries {
		m := e.(map[string]any)
		if int64(m["player_id"].(float64)) != order[i] || int64(m["rank"].(float64)) != int64(i+1) {
			t.Errorf("entry %d = %v", i, m)
		}
	}
	if me := page["me"].(map[string]any); me["rank"].(float64) != 3 {
		t.Errorf("me = %v", me)
	}
	rec = doReq(f.app.handleLeaderboardAround, "GET", "/v1/leaderboards/weekly_score/around?radius=1", nil, map[string]string{"name": "weekly_score"}, bearer(toks[2])...)
	if rec.Code != http.StatusOK {
		t.Fatalf("around status=%d body=%s", rec.Code, rec.Body.String())
	}
	around := decode(t, rec)
	if me := around["me"].(map[string]any); me["rank"].(float64) != 2 {
		t.Errorf("around me = %v", me)
	}
	if n := len(around["entries"].([]any)); n != 3 {
		t.Errorf("around entries = %d, want 3", n)
	}
	if _, err := f.app.toolStatsUpdate(f.ctx, map[string]any{"player_id": ids[0], "stat": "score", "value": 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.toolStatsUpdate(f.ctx, map[string]any{"player_id": ids[0], "stat": "score", "value": 10}); err != nil {
		t.Fatal(err)
	}
	toolPage, err := f.app.toolLeaderboardsGet(f.ctx, map[string]any{"name": "weekly_score", "player_id": ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	tp := toolPage.(*leaderboardPage)
	if (tp.Total == nil || *tp.Total != 3) || tp.Me == nil || tp.Me.Rank != 1 || tp.Me.Score != 100 {
		t.Errorf("tool page = total %d me %+v", *tp.Total, tp.Me)
	}
	if err := dbUpsertEntry(f.ctx.AppDB(), f.pid, lb.ID, "2000-W01", ids[1], 999); err != nil {
		t.Fatal(err)
	}
	rec = doReq(f.app.handleLeaderboardGet, "GET", "/v1/leaderboards/weekly_score?period=2000-W01", nil, map[string]string{"name": "weekly_score"}, bearer(toks[0])...)
	hist := decode(t, rec)
	if hist["period"] != "2000-W01" || len(hist["entries"].([]any)) != 1 {
		t.Errorf("history page = %v", hist)
	}
	if rec := doReq(f.app.handleLeaderboardGet, "GET", "/v1/leaderboards/nope", nil, map[string]string{"name": "nope"}, bearer(toks[0])...); rec.Code != http.StatusNotFound {
		t.Errorf("unknown board status=%d", rec.Code)
	}
}

func TestLeaderboards_RolloverEmitsResetAndSumRestarts(t *testing.T) {
	f := newFixture(t)
	if _, err := f.app.toolStatsDefine(f.ctx, map[string]any{"name": "points", "aggregation": "sum"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.toolLeaderboardsCreate(f.ctx, map[string]any{"name": "daily-points", "stat": "points", "reset": "daily"}); err != nil {
		t.Fatal(err)
	}
	out, tok := f.loginDevice(t, "dev-r", "Ro")
	pid := playerID(t, out)
	db := f.ctx.AppDB()
	lb, _ := dbGetLeaderboard(db, f.pid, "daily-points")
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02")
	if err := dbSetLeaderboardPeriod(db, f.pid, lb.ID, yesterday, time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := dbUpsertEntry(db, f.pid, lb.ID, yesterday, pid, 42); err != nil {
		t.Fatal(err)
	}
	f.events.Reset()
	if err := rolloverLeaderboards(f.ctx, f.pid, time.Now()); err != nil {
		t.Fatal(err)
	}
	resets := f.delivered(t).EventsByTopic("leaderboard.reset")
	if len(resets) != 1 {
		t.Fatalf("leaderboard.reset emitted %d times", len(resets))
	}
	if data := resets[0].Data.(map[string]any); data["previous_period"] != yesterday || data["manual"] != false {
		t.Errorf("reset payload = %v", data)
	}
	for _, inc := range []float64{5, 3} {
		if _, err := f.app.toolStatsUpdate(f.ctx, map[string]any{"player_id": pid, "stat": "points", "value": inc}); err != nil {
			t.Fatal(err)
		}
	}
	rec := doReq(f.app.handleLeaderboardGet, "GET", "/v1/leaderboards/daily-points", nil, map[string]string{"name": "daily-points"}, bearer(tok)...)
	today := decode(t, rec)
	if today["period"] == yesterday {
		t.Fatalf("board still on yesterday: %v", today)
	}
	if e := today["entries"].([]any); len(e) != 1 || e[0].(map[string]any)["score"].(float64) != 8 {
		t.Errorf("today's entries = %v, want one entry with 8", e)
	}
	rec = doReq(f.app.handleLeaderboardGet, "GET", "/v1/leaderboards/daily-points?period="+yesterday, nil, map[string]string{"name": "daily-points"}, bearer(tok)...)
	if e := decode(t, rec)["entries"].([]any); len(e) != 1 || e[0].(map[string]any)["score"].(float64) != 42 {
		t.Errorf("yesterday's entries = %v, want one entry with 42", e)
	}
}

func TestLeaderboards_ManualResetStartsFreshPeriod(t *testing.T) {
	f := newFixture(t)
	if _, err := f.app.toolLeaderboardsCreate(f.ctx, map[string]any{"name": "alltime", "stat": "score"}); err != nil {
		t.Fatal(err)
	}
	if def, _ := dbGetStatDef(f.ctx.AppDB(), f.pid, "score"); def == nil || def.Aggregation != "max" {
		t.Fatalf("leaderboard should auto-define its stat as max, got %+v", def)
	}
	out, _ := f.loginDevice(t, "dev-m", "Mo")
	pid := playerID(t, out)
	if _, err := f.app.toolStatsUpdate(f.ctx, map[string]any{"player_id": pid, "stat": "score", "value": 10}); err != nil {
		t.Fatal(err)
	}
	res, err := f.app.toolLeaderboardsResetNow(f.ctx, map[string]any{"name": "alltime"})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	lb := m["leaderboard"].(*Leaderboard)
	if m["previous_period"] != "all" || !strings.HasPrefix(lb.CurrentPeriod, "all-r") {
		t.Fatalf("reset result = %v", m)
	}
	if err := rolloverLeaderboards(f.ctx, f.pid, time.Now()); err != nil {
		t.Fatal(err)
	}
	if again, _ := dbGetLeaderboard(f.ctx.AppDB(), f.pid, "alltime"); again.CurrentPeriod != lb.CurrentPeriod {
		t.Errorf("scheduled rollover undid the manual reset: %q -> %q", lb.CurrentPeriod, again.CurrentPeriod)
	}
	if _, err := f.app.toolStatsUpdate(f.ctx, map[string]any{"player_id": pid, "stat": "score", "value": 20}); err != nil {
		t.Fatal(err)
	}
	cur, _ := f.app.toolLeaderboardsGet(f.ctx, map[string]any{"name": "alltime"})
	if p := cur.(*leaderboardPage); (p.Total == nil || *p.Total != 1) || p.Entries[0].Score != 20 {
		t.Errorf("current period page = %+v", p)
	}
	old, _ := f.app.toolLeaderboardsGet(f.ctx, map[string]any{"name": "alltime", "period": "all"})
	if p := old.(*leaderboardPage); (p.Total == nil || *p.Total != 1) || p.Entries[0].Score != 10 {
		t.Errorf("old period page = %+v", p)
	}
	resets := f.delivered(t).EventsByTopic("leaderboard.reset")
	if len(resets) != 1 || resets[0].Data.(map[string]any)["manual"] != true {
		t.Errorf("manual reset events = %v", resets)
	}
}

// ─── achievements ────────────────────────────────────────────────────

func TestAchievements_UnlockOnThresholdAndHidden(t *testing.T) {
	f := newFixture(t)
	if _, err := f.app.toolStatsDefine(f.ctx, map[string]any{"name": "wins", "aggregation": "sum"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.toolAchievementsDefine(f.ctx, map[string]any{"key": "first-win", "name": "First Win", "stat": "wins", "threshold": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.toolAchievementsDefine(f.ctx, map[string]any{"key": "secret", "name": "Secret", "stat": "wins", "threshold": 3, "hidden": true}); err != nil {
		t.Fatal(err)
	}
	out, tok := f.loginDevice(t, "dev-a", "Al")
	pid := playerID(t, out)
	rec := doReq(f.app.handleAchievementsGet, "GET", "/v1/achievements", nil, nil, bearer(tok)...)
	if items := decode(t, rec)["achievements"].([]any); len(items) != 1 || items[0].(map[string]any)["unlocked"] != false {
		t.Errorf("initial achievements = %v", items)
	}
	unlockedAfter := func(v float64) []string {
		res, err := f.app.toolStatsUpdate(f.ctx, map[string]any{"player_id": pid, "stat": "wins", "value": v})
		if err != nil {
			t.Fatal(err)
		}
		return res.(*statOutcome).Unlocked
	}
	if u := unlockedAfter(1); len(u) != 1 || u[0] != "first-win" {
		t.Errorf("first update unlocked %v", u)
	}
	if u := unlockedAfter(1); len(u) != 0 {
		t.Errorf("second update unlocked %v, want none", u)
	}
	if u := unlockedAfter(1); len(u) != 1 || u[0] != "secret" {
		t.Errorf("third update unlocked %v, want secret", u)
	}
	rec = doReq(f.app.handleAchievementsGet, "GET", "/v1/achievements", nil, nil, bearer(tok)...)
	items := decode(t, rec)["achievements"].([]any)
	if len(items) != 2 {
		t.Fatalf("achievements after unlocks = %v", items)
	}
	for _, it := range items {
		m := it.(map[string]any)
		if m["unlocked"] != true || m["unlocked_at"] == "" {
			t.Errorf("achievement %v not unlocked", m)
		}
	}
	if n := len(f.delivered(t).EventsByTopic("achievement.unlocked")); n != 2 {
		t.Errorf("achievement.unlocked emitted %d times", n)
	}
	if _, err := f.app.toolAchievementsDefine(f.ctx, map[string]any{"key": "beta", "name": "Beta tester"}); err != nil {
		t.Fatal(err)
	}
	g, err := f.app.toolAchievementsGrant(f.ctx, map[string]any{"player_id": pid, "key": "beta"})
	if err != nil || g.(map[string]any)["unlocked"] != true {
		t.Errorf("grant = %v, %v", g, err)
	}
	g, _ = f.app.toolAchievementsGrant(f.ctx, map[string]any{"player_id": pid, "key": "beta"})
	if g.(map[string]any)["unlocked"] != false {
		t.Errorf("second grant should be a no-op: %v", g)
	}
	if _, err := f.app.toolAchievementsGrant(f.ctx, map[string]any{"player_id": pid, "key": "nope"}); err == nil {
		t.Error("granting an undefined achievement must fail")
	}
}

// ─── bans, export, erase ─────────────────────────────────────────────

func TestBan_BlocksPlayerAndSyncsAuth(t *testing.T) {
	f := newFixture(t)
	out, tok := f.loginDevice(t, "dev-b", "Bad")
	pid := playerID(t, out)
	uid := int64(out["player"].(map[string]any)["auth_user_id"].(float64))

	if _, err := f.app.toolPlayersBan(f.ctx, map[string]any{"player_id": pid, "reason": "cheating", "expires_at": "not-a-date"}); err == nil {
		t.Fatal("bad expires_at must fail")
	}
	if _, err := f.app.toolPlayersBan(f.ctx, map[string]any{"player_id": pid}); err == nil {
		t.Fatal("ban without reason must fail")
	}
	res, err := f.app.toolPlayersBan(f.ctx, map[string]any{
		"player_id": pid, "reason": "cheating", "expires_at": time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ban := res.(map[string]any)["ban"].(*Ban); ban.Reason != "cheating" || ban.ExpiresAt == "" {
		t.Errorf("ban = %+v", ban)
	}
	if f.auth.disabled[uid] {
		t.Error("game ban must not disable the Auth account")
	}
	rec := doReq(f.app.handleMe, "GET", "/v1/me", nil, nil, bearer(tok)...)
	if rec.Code != http.StatusForbidden || decode(t, rec)["reason"] != "cheating" {
		t.Errorf("banned /v1/me status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = doReq(f.app.handleLoginDevice, "POST", "/v1/login/device", map[string]any{"device_id": "dev-b"}, nil)
	if rec.Code != http.StatusForbidden || decode(t, rec)["error"] != "banned" {
		t.Errorf("banned login status=%d body=%s", rec.Code, rec.Body.String())
	}
	ctxOut, err := f.app.toolPlayersGetContext(f.ctx, map[string]any{"device_id": "dev-b"})
	if err != nil {
		t.Fatal(err)
	}
	if ctxOut.(map[string]any)["active_ban"].(*Ban) == nil {
		t.Error("context should show the active ban")
	}
	search, _ := f.app.toolPlayersSearch(f.ctx, map[string]any{"status": "banned"})
	if n, _ := search.(map[string]any)["total"].(int); n != 1 {
		t.Errorf("banned search total = %d", n)
	}
	if _, err := f.app.toolPlayersUnban(f.ctx, map[string]any{"player_id": pid}); err != nil {
		t.Fatal(err)
	}
	if f.auth.disabled[uid] {
		t.Error("auth user should be re-enabled")
	}
	if rec := doReq(f.app.handleMe, "GET", "/v1/me", nil, nil, bearer(tok)...); rec.Code != http.StatusOK {
		t.Errorf("unbanned /v1/me status=%d", rec.Code)
	}
	if b, u := len(f.delivered(t).EventsByTopic("player.banned")), len(f.delivered(t).EventsByTopic("player.unbanned")); b != 1 || u != 1 {
		t.Errorf("ban events = %d/%d", b, u)
	}
}

func TestBan_ExpiredLiftsLazily(t *testing.T) {
	f := newFixture(t)
	out, tok := f.loginDevice(t, "dev-x", "Ex")
	pid := playerID(t, out)
	uid := int64(out["player"].(map[string]any)["auth_user_id"].(float64))
	db := f.ctx.AppDB()
	if _, err := dbCreateBan(db, f.pid, pid, "old", "test", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	_ = dbSetPlayerStatus(db, f.pid, pid, "banned")
	f.auth.disabled[uid] = true
	rec := doReq(f.app.handleMe, "GET", "/v1/me", nil, nil, bearer(tok)...)
	if rec.Code != http.StatusOK {
		t.Fatalf("expired ban should lift, status=%d body=%s", rec.Code, rec.Body.String())
	}
	if p, _ := dbGetPlayer(db, f.pid, pid); p.Status != "active" {
		t.Errorf("status = %s", p.Status)
	}
	if !f.auth.disabled[uid] {
		t.Error("expiry must not re-enable an independently disabled Auth account")
	}
	if ev := f.delivered(t).EventsByTopic("player.unbanned"); len(ev) != 1 || ev[0].Data.(map[string]any)["source"] != "expiry" {
		t.Errorf("unban events = %v", ev)
	}
}

func TestExportAndErase(t *testing.T) {
	f := newFixture(t)
	out, tok := f.loginDevice(t, "dev-e", "Eve")
	pid := playerID(t, out)
	uid := int64(out["player"].(map[string]any)["auth_user_id"].(float64))
	if _, err := f.app.toolDataSet(f.ctx, map[string]any{"player_id": pid, "key": "save", "value": map[string]any{"lvl": 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.toolStatsUpdate(f.ctx, map[string]any{"player_id": pid, "stat": "score", "value": 9}); err != nil {
		t.Fatal(err)
	}
	exp, err := f.app.toolPlayersExport(f.ctx, map[string]any{"player_id": pid})
	if err != nil {
		t.Fatal(err)
	}
	e := exp.(map[string]any)["export"].(map[string]any)
	if len(e["data"].([]DataEntry)) != 1 || len(e["stats"].([]PlayerStat)) != 1 {
		t.Errorf("export = %v", e)
	}
	if _, err := f.app.toolPlayersErase(f.ctx, map[string]any{"player_id": pid}); err == nil {
		t.Fatal("erase without confirm must fail")
	}
	if _, err := f.app.toolPlayersErase(f.ctx, map[string]any{"player_id": pid, "confirm": true}); err != nil {
		t.Fatal(err)
	}
	rec := doReq(f.app.handleMe, "GET", "/v1/me", nil, nil, bearer(tok)...)
	if rec.Code != http.StatusUnauthorized || decode(t, rec)["error"] != "player_not_found" {
		t.Errorf("erased /v1/me status=%d body=%s", rec.Code, rec.Body.String())
	}
	if f.auth.disabled[uid] || f.auth.callCount("auth.auth_users_revoke_sessions") != 0 {
		t.Error("game erasure must not modify the shared Auth account")
	}
	if p, _ := dbGetPlayer(f.ctx.AppDB(), f.pid, pid); p != nil {
		t.Error("player row should be gone")
	}
	if n := len(f.delivered(t).EventsByTopic("player.erased")); n != 1 {
		t.Errorf("player.erased emitted %d times", n)
	}
}

// ─── profiles, search, admin ─────────────────────────────────────────

func TestPlayersSearchUpdateAndPublicProfile(t *testing.T) {
	f := newFixture(t)
	f.loginDevice(t, "d1", "Alpha")
	f.loginDevice(t, "d2", "Beta")
	res, _ := f.app.toolPlayersSearch(f.ctx, map[string]any{"q": "alp"})
	if n, _ := res.(map[string]any)["count"].(int); n != 1 {
		t.Errorf("search alp count = %d", n)
	}
	res, _ = f.app.toolPlayersSearch(f.ctx, map[string]any{})
	if n, _ := res.(map[string]any)["total"].(int); n != 2 {
		t.Errorf("search all total = %d", n)
	}
	upd, err := f.app.toolPlayersUpdate(f.ctx, map[string]any{
		"device_id": "d2", "display_name": "Beta Prime", "region": "EU", "metadata": map[string]any{"title": "Knight"},
	})
	if err != nil {
		t.Fatal(err)
	}
	beta := upd.(map[string]any)["player"].(*Player)
	if beta.DisplayName != "Beta Prime" || beta.Region != "EU" || !strings.Contains(string(beta.Metadata), "Knight") {
		t.Errorf("updated player = %+v", beta)
	}
	if _, err := f.app.toolPlayersUpdate(f.ctx, map[string]any{"device_id": "d2", "avatar_url": "ftp://x"}); err == nil {
		t.Error("bad avatar_url must fail")
	}
	if _, err := f.app.toolPlayersUpdate(f.ctx, map[string]any{"device_id": "d2"}); err == nil {
		t.Error("empty update must fail")
	}
	if _, err := f.app.toolPlayersGet(f.ctx, map[string]any{"device_id": "never-seen"}); err == nil {
		t.Error("unknown device must fail")
	}
	_, tokA := f.loginDevice(t, "d1", "")
	rec := doReq(f.app.handlePublicPlayer, "GET", "/v1/players/"+idStr(beta.ID), nil, map[string]string{"id": idStr(beta.ID)}, bearer(tokA)...)
	if rec.Code != http.StatusOK {
		t.Fatalf("public profile status=%d body=%s", rec.Code, rec.Body.String())
	}
	if p := decode(t, rec)["player"].(map[string]any); p["display_name"] != "Beta Prime" || p["auth_user_id"] != nil {
		t.Errorf("public profile = %v", p)
	}
	rec = doReq(f.app.handleMePatch, "PATCH", "/v1/me", map[string]any{"display_name": "A1", "locale": "fr", "status": "banned"}, nil, bearer(tokA)...)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch me status=%d body=%s", rec.Code, rec.Body.String())
	}
	if p := decode(t, rec)["player"].(map[string]any); p["display_name"] != "A1" || p["locale"] != "fr" || p["status"] != "active" {
		t.Errorf("patched me = %v", p)
	}
	if rec := doReq(f.app.handleMePatch, "PATCH", "/v1/me", map[string]any{}, nil, bearer(tokA)...); rec.Code != http.StatusBadRequest {
		t.Errorf("empty patch status=%d", rec.Code)
	}
}

func TestAdminRoutes_PanelSurface(t *testing.T) {
	f := newFixture(t)
	f.loginDevice(t, "d1", "Alpha")
	rec := doReq(f.app.handleAdminStats, "GET", "/admin/stats", nil, nil)
	if rec.Code != http.StatusOK || decode(t, rec)["players"].(map[string]any)["total"].(float64) != 1 {
		t.Fatalf("admin stats status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doReq(f.app.handleAdminStatDefsUpsert, "POST", "/admin/stat-defs", map[string]any{"name": "score", "aggregation": "max"}, nil); rec.Code != http.StatusOK {
		t.Fatalf("stat def status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doReq(f.app.handleAdminLeaderboardsCreate, "POST", "/admin/leaderboards", map[string]any{"name": "top", "stat": "score"}, nil); rec.Code != http.StatusCreated {
		t.Fatalf("leaderboard create status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doReq(f.app.handleAdminLeaderboardsCreate, "POST", "/admin/leaderboards", map[string]any{"name": "top", "stat": "score"}, nil); rec.Code != http.StatusConflict {
		t.Errorf("duplicate leaderboard status=%d", rec.Code)
	}
	rec = doReq(f.app.handleAdminPlayersList, "GET", "/admin/players?q=alp", nil, nil)
	list := decode(t, rec)
	if rec.Code != http.StatusOK || list["total"].(float64) != 1 {
		t.Fatalf("players list status=%d body=%s", rec.Code, rec.Body.String())
	}
	id := idStr(int64(list["players"].([]any)[0].(map[string]any)["id"].(float64)))
	pv := map[string]string{"id": id}
	if rec := doReq(f.app.handleAdminPlayerStats, "POST", "/admin/players/"+id+"/stats", map[string]any{"updates": []any{map[string]any{"stat": "score", "value": 12}}}, pv); rec.Code != http.StatusOK {
		t.Fatalf("admin stats write status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = doReq(f.app.handleAdminLeaderboardEntries, "GET", "/admin/leaderboards/top/entries", nil, map[string]string{"name": "top"})
	if rec.Code != http.StatusOK || len(decode(t, rec)["page"].(map[string]any)["entries"].([]any)) != 1 {
		t.Fatalf("entries status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doReq(f.app.handleAdminPlayerBan, "POST", "/admin/players/"+id+"/ban", map[string]any{"reason": "spam"}, pv); rec.Code != http.StatusOK {
		t.Fatalf("ban status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = doReq(f.app.handleAdminPlayerGet, "GET", "/admin/players/"+id, nil, pv)
	if rec.Code != http.StatusOK || decode(t, rec)["active_ban"] == nil {
		t.Fatalf("player get status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doReq(f.app.handleAdminPlayerUnban, "POST", "/admin/players/"+id+"/unban", nil, pv); rec.Code != http.StatusOK {
		t.Errorf("unban status=%d", rec.Code)
	}
	if rec := doReq(f.app.handleAdminPlayerPatch, "PATCH", "/admin/players/"+id, map[string]any{"display_name": "Alpha Prime"}, pv); rec.Code != http.StatusOK {
		t.Errorf("patch status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doReq(f.app.handleAdminAchievementsUpsert, "POST", "/admin/achievements", map[string]any{"key": "x", "name": "X", "stat": "score", "threshold": 10}, nil); rec.Code != http.StatusOK {
		t.Errorf("achievement status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doReq(f.app.handleAdminLeaderboardReset, "POST", "/admin/leaderboards/top/reset", nil, map[string]string{"name": "top"}); rec.Code != http.StatusOK {
		t.Errorf("reset status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = doReq(f.app.handleAdminSettings, "GET", "/admin/settings", nil, nil)
	if rec.Code != http.StatusOK || decode(t, rec)["auth_client_id"] != "akc_test" {
		t.Errorf("settings status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doReq(f.app.handleAdminPlayerGet, "GET", "/admin/players/99999", nil, map[string]string{"id": "99999"}); rec.Code != http.StatusNotFound {
		t.Errorf("missing player status=%d", rec.Code)
	}
}

func (f *fixture) delivered(t *testing.T) *tk.EmitRecorder {
	t.Helper()
	if err := drainOutbox(f.ctx, func(scope GameScope, topic string, payload map[string]any) error {
		f.ctx.EmitWithProject(topic, scope.ProjectID, payload)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return f.events
}
func resignClaims(priv ed25519.PrivateKey, token string, changes map[string]any) string {
	parts := strings.Split(token, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	_ = json.Unmarshal(raw, &claims)
	for k, v := range changes {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	b, _ := json.Marshal(claims)
	input := parts[0] + "." + base64.RawURLEncoding.EncodeToString(b)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(input)))
}
