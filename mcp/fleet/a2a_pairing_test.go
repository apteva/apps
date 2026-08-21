package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type recordedA2ACall struct {
	Tool string
	Args map[string]any
}

type fleetA2APlatform struct {
	tk.BasePlatformClient
	mu       sync.Mutex
	bindings map[string]any
	calls    []recordedA2ACall
}

func (p *fleetA2APlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{InstallID: 901, Bindings: p.bindings}, nil
}

func (p *fleetA2APlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	if appName != "a2a" {
		return tk.ErrNotImplemented
	}
	p.mu.Lock()
	p.calls = append(p.calls, recordedA2ACall{Tool: tool, Args: input})
	p.mu.Unlock()
	switch tool {
	case "node_info":
		return decodeTestValue(map[string]any{
			"node_id": "main-node", "name": "Main", "public_url": "https://main.example/api/apps/a2a",
		}, out)
	case "peer_upsert":
		return decodeTestValue(map[string]any{"peer": input}, out)
	case "peer_remove":
		return decodeTestValue(map[string]any{"removed": true}, out)
	default:
		return tk.ErrNotImplemented
	}
}

func decodeTestValue(value, out any) error {
	raw, _ := json.Marshal(value)
	return json.Unmarshal(raw, out)
}

type tenantA2AFixture struct {
	mu              sync.Mutex
	installed       bool
	installRequests int
	configWrites    int
	defaultWrites   int
	agentWrites     int
	config          map[string]any
	server          *httptest.Server
}

func newTenantA2AFixture(t *testing.T) *tenantA2AFixture {
	t.Helper()
	f := &tenantA2AFixture{config: map[string]any{
		"public_url": "",
		"peers_json": `[{"id":"operator-peer","name":"Operator","base_url":"https://operator.example/a2a","token":"operator-token"}]`,
	}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tenant-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps":
			if !f.installed {
				_ = json.NewEncoder(w).Encode([]any{})
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"install_id": 44, "name": "a2a", "version": "0.4.0", "project_id": "", "status": "running",
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/install":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["project_id"] != "" || body["manifest_url"] != a2aManifestURL {
				http.Error(w, "bad global A2A install request", http.StatusBadRequest)
				return
			}
			f.installed = true
			f.installRequests++
			if cfg, ok := body["config"].(map[string]any); ok {
				f.config = cfg
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"install_id": 44, "status": "pending"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/installs/44/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"config": f.config})
		case r.Method == http.MethodPut && r.URL.Path == "/api/apps/installs/44/config":
			var body struct {
				Config map[string]any `json:"config"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.config = body.Config
			f.configWrites++
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/apps/installs/44/agent-default":
			f.defaultWrites++
			_ = json.NewEncoder(w).Encode(map[string]any{"default_for_new_agents": true})
		case r.Method == http.MethodGet && r.URL.Path == "/api/mcp-servers":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 77, "source": "app", "upstream_id": "app:44"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/agents":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 10}, {"id": 11}})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/agents/") && strings.HasSuffix(r.URL.Path, "/mcp-servers"):
			f.agentWrites++
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.RequestURI(), http.StatusNotFound)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func seedA2ATenant(t *testing.T, app *App, baseURL string, kind string) string {
	t.Helper()
	enc, err := app.keys.seal([]byte("tenant-key"))
	if err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{
		Slug: "tenant-one", Kind: kind, BaseURL: baseURL, ConfigDir: t.TempDir(),
		OwnerEmail: "owner@example.com", Status: StatusActive,
	}
	if err := app.store.insert(tenant, enc, nil); err != nil {
		t.Fatal(err)
	}
	return tenant.ID
}

func TestReconcileTenantA2AInstallsPairsAndAttaches(t *testing.T) {
	platform := &fleetA2APlatform{bindings: map[string]any{"a2a": int64(55)}}
	fixture := newTenantA2AFixture(t)
	app, ctx := newTestApp(t,
		tk.WithPlatform(platform),
		tk.WithConfig(map[string]string{
			"a2a_main_agents_json":   `["support"]`,
			"a2a_tenant_agents_json": `["worker-*"]`,
		}),
	)
	id := seedA2ATenant(t, app, fixture.server.URL, KindLocal)

	result := app.reconcileTenantA2A(ctx, id)
	if result.Status != "paired" || result.TenantInstallID != 44 || result.TenantAgentsAttached != 2 {
		t.Fatalf("unexpected pair result: %+v", result)
	}
	fixture.mu.Lock()
	if fixture.installRequests != 1 || fixture.defaultWrites != 1 || fixture.agentWrites != 2 {
		t.Fatalf("tenant calls install=%d default=%d agents=%d", fixture.installRequests, fixture.defaultWrites, fixture.agentWrites)
	}
	configuredPeers, err := parseFleetA2APeers(fixture.config["peers_json"].(string))
	fixture.mu.Unlock()
	if err != nil || len(configuredPeers) != 1 {
		t.Fatalf("configured peers=%+v err=%v", configuredPeers, err)
	}
	var tenantPeer fleetA2APeer
	for _, peer := range configuredPeers {
		if strings.HasPrefix(peer.ID, "fleet-main:") {
			tenantPeer = peer
		}
	}
	if tenantPeer.BaseURL != "https://main.example/api/apps/a2a" || len(tenantPeer.DiscoverAgents) != 1 || tenantPeer.DiscoverAgents[0] != "worker-*" {
		t.Fatalf("bad tenant peer: %+v", tenantPeer)
	}

	platform.mu.Lock()
	var parentPeer map[string]any
	for _, call := range platform.calls {
		if call.Tool == "peer_upsert" {
			parentPeer = call.Args
		}
	}
	platform.mu.Unlock()
	if parentPeer["base_url"] != fixture.server.URL+"/api/apps/a2a" {
		t.Fatalf("parent peer URL=%v", parentPeer["base_url"])
	}
	if parentPeer["token"] == "" || parentPeer["token"] != tenantPeer.Token {
		t.Fatalf("relationship token was not shared symmetrically")
	}
	if got := parentPeer["discover_agents"].([]string); len(got) != 1 || got[0] != "support" {
		t.Fatalf("parent policy=%v", got)
	}

	second := app.reconcileTenantA2A(ctx, id)
	if second.Status != "paired" || second.Reason != "already reconciled" {
		t.Fatalf("second reconcile=%+v", second)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.installRequests != 1 || fixture.agentWrites != 2 {
		t.Fatalf("idempotent reconcile repeated tenant writes: %+v", fixture)
	}
}

func TestReconcileTenantA2AIsOptionalAndSkipsExternalTenants(t *testing.T) {
	t.Run("no parent binding", func(t *testing.T) {
		platform := &fleetA2APlatform{bindings: map[string]any{}}
		app, ctx := newTestApp(t, tk.WithPlatform(platform))
		id := seedA2ATenant(t, app, "http://127.0.0.1:1", KindLocal)
		if got := app.reconcileTenantA2A(ctx, id); got.Status != "disabled" {
			t.Fatalf("result=%+v", got)
		}
	})
	t.Run("external tenant", func(t *testing.T) {
		platform := &fleetA2APlatform{bindings: map[string]any{"a2a": int64(55)}}
		app, ctx := newTestApp(t, tk.WithPlatform(platform))
		id := seedA2ATenant(t, app, "https://external.example", KindRemote)
		if got := app.reconcileTenantA2A(ctx, id); got.Status != "skipped" {
			t.Fatalf("result=%+v", got)
		}
	})
}

func TestFleetA2ATokenIsStableAndTenantScoped(t *testing.T) {
	app, _ := newTestApp(t)
	one := app.keys.deriveToken("a2a-peer:one")
	if one == "" || one != app.keys.deriveToken("a2a-peer:one") {
		t.Fatal("derived A2A token is empty or unstable")
	}
	if one == app.keys.deriveToken("a2a-peer:two") {
		t.Fatal("different tenants received the same A2A token")
	}
}

func TestResetLocalClonedA2AStateOnlyRemovesA2AIdentity(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "apteva.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE apps (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE app_installs (id INTEGER PRIMARY KEY, app_id INTEGER NOT NULL, project_id TEXT)`,
		`INSERT INTO apps(id,name) VALUES (1,'a2a'),(2,'notes')`,
		`INSERT INTO app_installs(id,app_id,project_id) VALUES (44,1,''),(45,2,'')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	a2aDir := filepath.Join(root, "apps", "a2a", "data", "44")
	notesDir := filepath.Join(root, "apps", "notes", "data", "45")
	if err := os.MkdirAll(a2aDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a2aDir, "app.db"), []byte("copied identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notesDir, "app.db"), []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	changed, err := resetLocalClonedA2AState(root)
	if err != nil || !changed {
		t.Fatalf("reset changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(a2aDir); !os.IsNotExist(err) {
		t.Fatalf("A2A identity dir still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(notesDir, "app.db")); err != nil {
		t.Fatalf("unrelated app data was touched: %v", err)
	}
}
