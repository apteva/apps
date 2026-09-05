package main

// Regression tests for the v0.10.5 audit: PASS requires safe behavior.
// All filesystem changes and listeners are isolated to test temporary paths.
import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func auditArchive(t *testing.T, prefix string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	entries := []struct {
		name, link, body string
		kind             byte
	}{
		{prefix + "a", ".", "", tar.TypeSymlink},
		{prefix + "a/b", "..", "", tar.TypeSymlink},
		{prefix + "a/b/escaped.txt", "", "escaped", tar.TypeReg},
	}
	if prefix != "" {
		entries = append(entries, struct {
			name, link, body string
			kind             byte
		}{"manifest.json", "", `{"format_version":1}`, tar.TypeReg})
	}
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Linkname: e.link, Typeflag: e.kind, Mode: 0700, Size: int64(len(e.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAuditStreamArchiveEscapesDestination(t *testing.T) {
	root := t.TempDir()
	if err := extractTenantArchiveStream(bytes.NewReader(auditArchive(t, "")), filepath.Join(root, "target")); err == nil {
		t.Fatal("escaping archive accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatal("archive escaped destination")
	}
}

func TestAuditBackupArchiveEscapesPayload(t *testing.T) {
	root := t.TempDir()
	if _, _, err := extractFleetTenantArchive(bytes.NewReader(auditArchive(t, "tenant/")), root); err == nil {
		t.Fatal("escaping archive accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatal("archive escaped payload")
	}
}

func TestAuditCloneDeletesPreexistingTarget(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "source", StatusStopped)
	src, _, _ := app.store.get(id)
	if err := os.MkdirAll(src.ConfigDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src.ConfigDir, "data"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(localDataRoot(), "existing")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "valuable-data")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := app.toolClone(ctx, map[string]any{"source_tenant_id": id, "slug": "existing", "start": false})
	if err == nil {
		t.Fatal("expected target-exists failure")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("preexisting target lost: %v", err)
	}
}

func TestAuditScopeMatchesNeighbor(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "systemctl")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' 'fleet-tenant-acme-100.scope loaded active running' 'fleet-tenant-acme-other-101.scope loaded active running'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	units, err := listTenantScopeUnits(fake, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 0 {
		t.Fatalf("unsafe legacy scope accepted: %v", units)
	}
}

func TestAuditStopReturnsSuccessWithLiveListener(t *testing.T) {
	app, _ := newTestApp(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	t.Setenv("PATH", t.TempDir())
	if err := app.stopTenantBy("audit-only", filepath.Join(localDataRoot(), "audit-only"), port, time.Millisecond); err == nil {
		t.Fatal(err)
	}
	if !portInUse(port) {
		t.Fatal("listener unexpectedly stopped")
	}
}

func TestAuditPublicStatusAcceptsWrongAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/status" {
			http.Error(w, "unauthorized", 401)
			return
		}
		_, _ = w.Write([]byte(`{"needs_setup":false,"reg_mode":"closed"}`))
	}))
	defer srv.Close()
	if err := verifyAPIKey(context.Background(), srv.URL, "invalid-key"); err == nil {
		t.Fatal(err)
	}
}

func TestAuditHealthSuccessDoesNotResetFailures(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "health", StatusActive)
	tn, _, _ := app.store.get(id)
	for i := 0; i < failuresToDisconnect-1; i++ {
		app.bumpFailures(ctx, tn)
	}
	if err := app.store.updateHealth(id, true, "0.42.0", []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	app.bumpFailures(ctx, tn)
	got, _, _ := app.store.get(id)
	if got.Status != StatusActive {
		t.Fatalf("success did not reset failure streak: %s", got.Status)
	}
}

func TestAuditStartLosesSetupPending(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "pending", StatusSetupPending)
	srv := fakeTenantServer(t, true)
	tn, _, _ := app.store.get(id)
	if err := app.store.setLocation(id, 0, srv.URL, tn.ConfigDir); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolStart(ctx, map[string]any{"tenant_id": id}); err == nil {
		t.Fatal("adopted an unverified setup listener")
	}
	got, _, _ := app.store.get(id)
	if got.Status != StatusSetupPending {
		t.Fatalf("setup state lost: %s", got.Status)
	}
}

func TestAuditQuarantineForgottenAfterEventRetention(t *testing.T) {
	app, _ := newTestApp(t)
	id := seedTenant(t, app, "clone", StatusStopped)
	if err := app.store.recordEvent(id, "cloned", "test", nil); err != nil {
		t.Fatal(err)
	}
	if required, _ := app.cloneRequiresQuarantine(id); !required {
		t.Fatal("initial quarantine missing")
	}
	for i := 0; i < 1000; i++ {
		if err := app.store.recordEvent(id, "test", "test", nil); err != nil {
			t.Fatal(err)
		}
	}
	if required, err := app.cloneRequiresQuarantine(id); err != nil || !required {
		t.Fatalf("quarantine forgotten: %v %v", required, err)
	}
}

type auditErrorPlatform struct{ tk.BasePlatformClient }

func (p *auditErrorPlatform) CallApp(string, string, map[string]any) (json.RawMessage, error) {
	return json.RawMessage(`{"result":{"isError":true,"content":[{"type":"text","text":"provider rejected mutation"}]}}`), nil
}
func TestAuditMCPToolErrorSwallowed(t *testing.T) {
	_, ctx := newTestApp(t, tk.WithPlatform(&auditErrorPlatform{}))
	if err := callSiblingTool(ctx, "domains", "p", "record_delete", nil, nil); err == nil {
		t.Fatal(err)
	}
	if _, err := unwrapMCP([]byte(`{"result":{"isError":true,"content":[{"type":"text","text":"failed"}]}}`)); err == nil {
		t.Fatal(err)
	}
}

func TestAuditDeleteLeavesPrimaryIngress(t *testing.T) {
	pf := &fleetIngressPlatform{}
	app, ctx := newTestApp(t, tk.WithPlatform(pf))
	id := seedTenant(t, app, "delete", StatusStopped)
	tn, _, _ := app.store.get(id)
	if err := app.attachDomain(ctx, "p", tn, attachDomainSpec{FQDN: "tenant.example.com", ManageDNS: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolDelete(ctx, map[string]any{"tenant_id": id, "confirm": true}); err != nil {
		t.Fatal(err)
	}
	if len(pf.exposed) != 1 || len(pf.unexposed) != 1 {
		t.Fatalf("primary ingress not removed: %+v", pf)
	}
}

func TestAuditRestoreCopyDereferencesSymlink(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	if err := os.MkdirAll(src, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "data"), []byte("value"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("data", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := copyDir(src, dst); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(dst, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was dereferenced")
	}
}

func TestAuditTenantReceivesFleetCredentials(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "test-fleet-token")
	t.Setenv("FLEET_MASTER_KEY", "test-master-key")
	env := strings.Join(tenantSpawnEnv(t.TempDir(), 34567, "tenant-a"), "\n")
	if strings.Contains(env, "test-fleet-token") || strings.Contains(env, "test-master-key") {
		t.Fatal("Fleet credential leaked")
	}
}

var _ sdk.PlatformClient = (*auditErrorPlatform)(nil)

func TestAuditSidecarArbitrarySignatureBypassesReadAuth(t *testing.T) {
	root := t.TempDir()
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("audit"), tk.WithEnv("FLEET_DATA_ROOT", filepath.Join(root, "fleet")), tk.WithEnv("APTEVA_DATA_DIR", root))
	for _, c := range []struct {
		path   string
		status int
	}{{"/tenants", 401}, {"/tenants?sig=invalid", 401}} {
		resp, err := http.Get(sc.URL() + c.path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.status {
			t.Fatalf("%s: got %d want %d", c.path, resp.StatusCode, c.status)
		}
	}
}

func TestAuditTenantPortRangesOverlap(t *testing.T) {
	app, _ := newTestApp(t)
	one := seedTenant(t, app, "one", StatusStopped)
	two := seedTenant(t, app, "two", StatusStopped)
	a, err := app.reserveAppPortBlock(one, 0, 6000)
	if err != nil {
		t.Fatal(err)
	}
	b, err := app.reserveAppPortBlock(two, 0, 6001)
	if err != nil {
		t.Fatal(err)
	}
	if a+999 >= b && b+999 >= a {
		t.Fatal("application ranges overlap")
	}
}

func TestAuditPrimaryDomainCanBelongToTwoTenants(t *testing.T) {
	pf := &fleetIngressPlatform{}
	app, ctx := newTestApp(t, tk.WithPlatform(pf))
	for i, slug := range []string{"one", "two"} {
		id := seedTenant(t, app, slug, StatusStopped)
		tn, _, _ := app.store.get(id)
		err := app.attachDomain(ctx, "p", tn, attachDomainSpec{FQDN: "shared.example.com", ManageDNS: false})
		if (i == 0 && err != nil) || (i == 1 && err == nil) {
			t.Fatalf("domain ownership: %v", err)
		}
	}
	if len(pf.exposed) != 1 {
		t.Fatal("conflicting ingress published")
	}
}

func TestAuditStoppedTenantPortCanBeReassigned(t *testing.T) {
	app, ctx := newTestApp(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	id := seedTenant(t, app, "reserved", StatusStopped)
	tn, _, _ := app.store.get(id)
	if err := app.store.setLocation(id, 0, "http://127.0.0.1:"+fmt.Sprint(port), tn.ConfigDir); err != nil {
		t.Fatal(err)
	}
	got, err := app.pickTenantPort(ctx, fleetHost{}, port)
	if err == nil || got != 0 {
		t.Fatalf("reserved port was reused: %d %v", got, err)
	}
}

func TestAuditMissingPinnedBinaryFallsBack(t *testing.T) {
	root := t.TempDir()
	fallback := filepath.Join(root, "host-apteva")
	if err := os.WriteFile(fallback, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_APTEVA_BIN", fallback)
	got, err := resolveAptevaBin(filepath.Join(root, "missing-pinned-version"))
	if err == nil || got != "" {
		t.Fatalf("missing explicit runtime fell back: %s %v", got, err)
	}
}

type auditTransport func(*http.Request) (*http.Response, error)

func (f auditTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAuditIngressProbeLeaksAdminKeyAndAccepts404(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	captured := ""
	http.DefaultTransport = auditTransport(func(r *http.Request) (*http.Response, error) {
		captured = r.Header.Get("Authorization")
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("unrelated app")), Header: make(http.Header), Request: r}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	code, err := verifyPublicHTTPS(ctx, "app.example.com", "/", "test-admin-key")
	if err == nil || captured != "" {
		t.Fatalf("admin credential leaked or HTTP 404 accepted: %d %v %q", code, err, captured)
	}
}

func TestAuditPartialSetupLosesGeneratedPassword(t *testing.T) {
	app, _ := newTestApp(t)
	registeredPassword := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/register":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			registeredPassword = body["password"]
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/auth/login":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/auth/keys":
			http.Error(w, "temporary failure", 503)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	result, err := app.autoSetupTenant(context.Background(), srv.URL, "test-setup", "admin@example.com", "")
	if err == nil || result == nil || result.Password != registeredPassword || registeredPassword == "" {
		t.Fatal("recovery password lost")
	}
}
