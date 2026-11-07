package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"os/exec"
	"strings"
	"testing"
)

func providerFixture() proxyIntent {
	return proxyIntent{Contract: proxyContract, ControllerID: "controller-a", OperationID: "op-1", InstallID: 52, DeploymentID: "deployment-1", Generation: 1, Target: nativeTarget{InstallID: 40, Type: "instances.instance", ID: "1"}, Hostname: "voice.example.test", HostKey: "linux-systemd:" + nativeHash("machine"), Upstream: "http://127.0.0.1:55001", Routes: []sdk.RuntimeIngressRoute{{Path: "/browser/*"}, {Path: "/carrier/*"}}, Options: nativeProxySpec{Adapter: "caddy", Hostname: "voice.example.test", Service: "caddy.service", Binary: "/usr/bin/caddy", Config: "/etc/caddy/Caddyfile", SiteFile: "/etc/caddy/sites/voice.caddy", Format: "caddyfile", Admin: "unix//run/caddy/admin.sock"}}
}
func TestProxyProviderContractIsIndependentOfNativeDeployment(t *testing.T) {
	r := providerFixture()
	if e := validateProxyIntent(r, "inspect"); e != nil {
		t.Fatal(e)
	}
	// No artifact, native execution, app manifest, environment or native backend record.
	p := proxyPayload(r, r.HostKey, 40, "inspect")
	raw := nativeJSON(p)
	if strings.Contains(raw, "APTEVA_APP_TOKEN") || strings.Contains(raw, "artifact") || !strings.Contains(raw, "stream_close_delay 24h") || !strings.Contains(raw, "/carrier/*") {
		t.Fatal(raw)
	}
	a := p["root"]
	r.ControllerID = "controller-b"
	if proxyPayload(r, r.HostKey, 40, "inspect")["root"] == a {
		t.Fatal("controllers share proxy ownership namespace")
	}
}
func TestProxyProviderRejectsUnsafeOrUnreviewedInput(t *testing.T) {
	r := providerFixture()
	for _, change := range []func(*proxyIntent){func(r *proxyIntent) { r.Options.Binary = "/usr/../bin/caddy" }, func(r *proxyIntent) { r.Options.Admin = "0.0.0.0:2019" }, func(r *proxyIntent) { r.Options.Service = "caddy.service; echo bad" }, func(r *proxyIntent) { r.Hostname = "bad\nhost" }, func(r *proxyIntent) { r.Contract = "wrong" }, func(r *proxyIntent) { r.Upstream = "https://remote.example" }, func(r *proxyIntent) { r.Routes = []sdk.RuntimeIngressRoute{{Path: "/_apteva/runtime/*"}} }} {
		v := r
		change(&v)
		if validateProxyIntent(v, "inspect") == nil {
			t.Fatal("accepted unsafe intent", nativeJSON(v))
		}
	}
	if validateProxyIntent(r, "adopt") == nil {
		t.Fatal("unreviewed adoption accepted")
	}
	r.PlanSHA256 = nativeHash("plan")
	r.MaintenanceWindow = true
	if e := validateProxyIntent(r, "adopt"); e != nil {
		t.Fatal(e)
	}
	r.HostKey = ""
	if validateProxyIntent(r, "rollback") == nil {
		t.Fatal("rollback without host identity")
	}
}
func TestProxyProviderManifestDeclaresProtectedRoute(t *testing.T) {
	// Both the embedded and standalone manifests must expose the provider to Server.
	// YAML parity is also covered by the app's existing manifest regression suite.
	app := &App{}
	found := false
	for _, r := range app.HTTPRoutes() {
		if r.Pattern == proxyPrefix+"/" {
			found = true
			if r.NoAuth {
				t.Fatal("proxy adapter must require SDK authentication")
			}
		}
	}
	if !found {
		t.Fatal("missing provider handler")
	}
	if _, e := json.Marshal(providerFixture()); e != nil {
		t.Fatal(e)
	}
}
func TestProxyAdoptionPythonAdapter(t *testing.T) {
	if _, e := exec.LookPath("python3"); e != nil {
		t.Skip("Python 3 unavailable")
	}
	if _, e := exec.LookPath("caddy"); e != nil {
		t.Skip("Caddy unavailable; run adapter integration suite before release")
	}
	cmd := exec.Command("python3", "-B", "-m", "unittest", "discover", "-s", "runtime", "-p", "test_proxy_adoption.py", "-v")
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("proxy adapter suite: %v\n%s", e, out)
	}
}

func TestProxyProviderResolvesHostInventoryWithoutNativeController(t *testing.T) {
	db := openTestDB(t)
	inst, e := dbCreateInstance(db, CreateInstanceInput{Name: "proxy-host", Provider: "external", Status: "ready", Platform: "linux", SSHHost: "host.test", SSHUser: "root"})
	if e != nil {
		t.Fatal(e)
	}
	r := providerFixture()
	r.Target.ID = fmt.Sprint(inst.ID)
	r.ProjectID = "project-a"
	if _, e = resolveProxyTarget(db, 40, "project-a", r); e != nil {
		t.Fatal(e)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM native_deployments`).Scan(&count)
	if count != 0 {
		t.Fatal("proxy lookup created native deployment records")
	}
	r.Target.InstallID = 41
	if _, e = resolveProxyTarget(db, 40, "project-a", r); e == nil {
		t.Fatal("wrong provider namespace accepted")
	}
	r.Target.InstallID = 40
	r.ProjectID = "project-b"
	if _, e = resolveProxyTarget(db, 40, "project-a", r); e == nil {
		t.Fatal("cross-project target accepted")
	}
	r.ProjectID = "project-a"
	r.Target.ID = "0"
	if _, e = resolveProxyTarget(db, 40, "project-a", r); e == nil {
		t.Fatal("local target accepted")
	}
}
