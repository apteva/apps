package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLifecycleRuntime(t *testing.T, root, version string, fail, safe, changed bool) aptevaRuntimePaths {
	t.Helper()
	r := writeFakeVersionedRuntime(t, root, version)
	cli := fmt.Sprintf(`#!/usr/bin/env python3
import http.server, json, os, sys
VERSION=%q
if "--version" in sys.argv:
    print("apteva " + VERSION)
    sys.exit(0)
home = sys.argv[sys.argv.index("--data-dir")+1]
port = int(sys.argv[sys.argv.index("--port")+1])
with open(os.path.join(home,"launched-"+VERSION+".json"),"w") as f: json.dump(sys.argv,f)
if "--server-bin" not in sys.argv: raise RuntimeError("legacy tenant was implicitly converted to stable host")
if %s: sys.exit(42)
class Health(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type","application/json")
        self.end_headers()
        self.wfile.write(json.dumps({"ok":True,"cli":VERSION,"apteva":VERSION}).encode())
    def log_message(self,*args): pass
http.server.HTTPServer(("127.0.0.1",port),Health).serve_forever()
`, version, pythonBool(fail))
	if err := os.WriteFile(r.CLI, []byte(cli), 0755); err != nil {
		t.Fatal(err)
	}
	server := fmt.Sprintf(`#!/usr/bin/env python3
import json, os, sys
if "--version" in sys.argv:
    print("apteva-server " + %q)
    sys.exit(0)
assert "--preflight" in sys.argv
path=os.environ["APTEVA_UPDATE_PREFLIGHT"]
with open(path+".json","w") as f: json.dump({"version":%q,"rollback_safe":%s,"schema_changed":%s},f)
`, version, version, pythonBool(safe), pythonBool(changed))
	if err := os.WriteFile(r.Server, []byte(server), 0755); err != nil {
		t.Fatal(err)
	}
	// New packages containing a host must still use the explicit server flag
	// for existing legacy installations.
	if err := os.WriteFile(filepath.Join(filepath.Dir(r.CLI), "apteva-host"), []byte("#!/bin/sh\nexit 99\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return r
}
func pythonBool(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

func TestLegacyUpdateAndCompatibleRollbackUseExactVersion(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			app, ctx := newTestApp(t)
			root := t.TempDir()
			t.Setenv("FLEET_VERSIONS_ROOT", root)
			writeLifecycleRuntime(t, root, "0.90.8", false, true, false)
			writeLifecycleRuntime(t, root, "0.91.0", fail, true, false)
			id := seedTenant(t, app, "legacy", StatusActive)
			port, err := allocatePort()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := app.store.db.Exec(`UPDATE fleet_tenants SET base_url=?,current_version=?,target_version=? WHERE id=?`, fmt.Sprintf("http://127.0.0.1:%d", port), "0.90.8", "0.90.8", id); err != nil {
				t.Fatal(err)
			}
			tn, _, _ := app.store.get(id)
			if err := os.MkdirAll(tn.ConfigDir, 0700); err != nil {
				t.Fatal(err)
			}
			sentinel := []byte("new writes must survive")
			if err := os.WriteFile(filepath.Join(tn.ConfigDir, "apteva.db"), sentinel, 0600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = app.stopTenantBy(tn.Slug, tn.ConfigDir, port, time.Second) })
			_, err = app.toolUpdate(ctx, map[string]any{"tenant_id": id, "version": "0.91.0"})
			want := "0.91.0"
			if fail {
				want = "0.90.8"
				if err == nil || !strings.Contains(err.Error(), "previous version restored") {
					t.Fatalf("rollback: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if _, err := requireRuntimeHealthVersion(context.Background(), tn.BaseURL, want); err != nil {
				t.Fatal(err)
			}
			args, err := os.ReadFile(filepath.Join(tn.ConfigDir, "launched-"+want+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(args), "--server-bin") {
				t.Fatal("legacy startup did not pin server mode")
			}
			got, _, _ := app.store.get(id)
			if got.CurrentVersion != want || got.TargetVersion != want {
				t.Fatalf("state: %+v", got)
			}
			data, _ := os.ReadFile(filepath.Join(tn.ConfigDir, "apteva.db"))
			if string(data) != string(sentinel) {
				t.Fatal("live database overwritten")
			}
		})
	}
}

func TestLegacyUpdateRefusesUnsafeRollback(t *testing.T) {
	for _, scenario := range []string{"candidate-migrates", "prior-incompatible", "missing-report"} {
		t.Run(scenario, func(t *testing.T) {
			app, ctx := newTestApp(t)
			root := t.TempDir()
			t.Setenv("FLEET_VERSIONS_ROOT", root)
			writeLifecycleRuntime(t, root, "0.90.8", false, scenario != "prior-incompatible", false)
			candidate := writeLifecycleRuntime(t, root, "0.91.0", true, scenario != "candidate-migrates", scenario == "candidate-migrates")
			if scenario == "missing-report" {
				if err := os.WriteFile(candidate.Server, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'apteva-server 0.91.0'; fi\nexit 0\n"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			id := seedTenant(t, app, "unsafe", StatusActive)
			if _, err := app.store.db.Exec(`UPDATE fleet_tenants SET current_version='0.90.8',target_version='0.90.8' WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
			tn, _, _ := app.store.get(id)
			if err := os.MkdirAll(tn.ConfigDir, 0700); err != nil {
				t.Fatal(err)
			}
			_, err := app.toolUpdate(ctx, map[string]any{"tenant_id": id, "version": "0.91.0"})
			if err == nil || !strings.Contains(err.Error(), "recovery required") {
				t.Fatalf("unsafe update: %v", err)
			}
			if _, err := os.Stat(filepath.Join(tn.ConfigDir, "launched-0.90.8.json")); !os.IsNotExist(err) {
				t.Fatal("older runtime launched against unconfirmed database")
			}
			if op, _ := app.store.activeOperation(id); op == nil || op.Phase != "recovery_required" {
				t.Fatalf("missing fence: %+v", op)
			}
		})
	}
}

func TestLegacyDowngradeRequiresCompatibilityBeforeStop(t *testing.T) {
	app, ctx := newTestApp(t)
	root := t.TempDir()
	t.Setenv("FLEET_VERSIONS_ROOT", root)
	writeLifecycleRuntime(t, root, "0.90.8", false, false, true)
	writeLifecycleRuntime(t, root, "0.91.0", false, true, false)
	id := seedTenant(t, app, "downgrade", StatusActive)
	if _, err := app.store.db.Exec(`UPDATE fleet_tenants SET current_version='0.91.0',target_version='0.91.0' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	tn, _, _ := app.store.get(id)
	if err := os.MkdirAll(tn.ConfigDir, 0700); err != nil {
		t.Fatal(err)
	}
	_, err := app.toolUpdate(ctx, map[string]any{"tenant_id": id, "version": "0.90.8"})
	if err == nil || !strings.Contains(err.Error(), "downgrade requires") {
		t.Fatalf("unsafe downgrade: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tn.ConfigDir, "launched-0.90.8.json")); !os.IsNotExist(err) {
		t.Fatal("downgrade started")
	}
}

func TestRuntimeHealthRequiresReadyAndUsesCLIVersion(t *testing.T) {
	ready := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": ready, "cli": "0.91.0", "apteva": "0.1.0"})
	}))
	defer srv.Close()
	if _, err := requireRuntimeHealthVersion(context.Background(), srv.URL, "0.91.0"); err == nil {
		t.Fatal("unready runtime accepted")
	}
	ready = true
	if _, err := requireRuntimeHealthVersion(context.Background(), srv.URL, "0.91.0"); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyUpdateUsesObservedServingVersionInsteadOfCachedRow(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "drift", StatusActive)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "cli": "0.91.0"})
	}))
	defer srv.Close()
	if _, err := app.store.db.Exec(`UPDATE fleet_tenants SET base_url=?,current_version='0.90.8',target_version='0.91.0' WHERE id=?`, srv.URL, id); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolUpdate(ctx, map[string]any{"tenant_id": id, "version": "0.91.0"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["updated"] != false {
		t.Fatalf("running version unnecessarily restarted: %v", out)
	}
	tn, _, _ := app.store.get(id)
	if tn.CurrentVersion != "0.91.0" {
		t.Fatalf("stale runtime identity: %s", tn.CurrentVersion)
	}
}
