package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDNSCapabilityIsTenantBoundAndRevocable(t *testing.T) {
	a, _ := newTestApp(t)
	id := seedTenant(t, a, "dns-a", StatusStopped)
	other := seedTenant(t, a, "dns-b", StatusStopped)
	if err := a.store.upsertDomainGrant(&DomainGrant{TenantID: id, Domain: "a.example.com", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	token, _, err := a.dnsToken(id)
	if err != nil {
		t.Fatal(err)
	}
	call := func(path, name string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":{"tenant_id":%q,"_project_id":"forged"}}}`, name, other)
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		a.httpDelegatedDNS(w, r)
		return w
	}
	if r := call("/dns/"+id+"/mcp", "tenant_domain_list"); r.Code != 200 || !strings.Contains(r.Body.String(), "a.example.com") {
		t.Fatalf("tenant-bound list: %s", r.Body.String())
	}
	if r := call("/dns/"+other+"/mcp", "tenant_domain_list"); r.Code != 401 {
		t.Fatalf("cross-tenant token accepted: %d", r.Code)
	}
	if r := call("/dns/"+id+"/mcp", "tenant_delete"); !strings.Contains(r.Body.String(), "does not authorize") {
		t.Fatal(r.Body.String())
	}
	if _, err = a.store.db.Exec(`UPDATE fleet_tenant_state SET dns_epoch=dns_epoch+1 WHERE tenant_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if r := call("/dns/"+id+"/mcp", "tenant_domain_list"); r.Code != 401 {
		t.Fatal("revoked capability accepted")
	}
}

func TestOperationSurvivesControllerRestartAndFencesStart(t *testing.T) {
	a, ctx := newTestApp(t)
	id := seedTenant(t, a, "durable", StatusStopped)
	done, err := a.beginTenantOperation(id, "migration")
	if err != nil {
		t.Fatal(err)
	}
	a.requireRecovery(id, errors.New("injected crash"))
	done()
	restarted := &App{}
	if err = restarted.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.beginTenantOperation(id, "start"); err == nil {
		t.Fatal("lost durable exclusion")
	}
	op, err := restarted.store.activeOperation(id)
	if err != nil || op == nil || op.Phase != "recovery_required" {
		t.Fatalf("operation=%+v error=%v", op, err)
	}
	w := httptest.NewRecorder()
	restarted.httpRecoverOperation(w, httptest.NewRequest("POST", "/tenants/"+id+"/recover-operation", strings.NewReader(`{"confirm":true}`)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	done, err = restarted.beginTenantOperation(id, "start")
	if err != nil {
		t.Fatal(err)
	}
	done()
}

func TestRecoveryRestoresDirectoryAndCredentialTogether(t *testing.T) {
	a, _ := newTestApp(t)
	id := seedTenant(t, a, "restore-crash", StatusStopped)
	tenant, _, _ := a.store.get(id)
	m, err := a.store.backupMetadata(id)
	if err != nil {
		t.Fatal(err)
	}
	previous := tenant.ConfigDir + ".prerestore-test"
	if err = os.MkdirAll(previous, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(previous, "data"), []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(tenant.ConfigDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(tenant.ConfigDir, "data"), []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	done, err := a.beginTenantOperation(id, "restore")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.checkpointOperation(id, "restore_swap", map[string]any{"source": tenant, "source_metadata": m, "previous_data_dir": previous}); err != nil {
		t.Fatal(err)
	}
	changed := m
	changed.APIKey = []byte("different")
	if err = a.store.restoreMetadata(id, changed); err != nil {
		t.Fatal(err)
	}
	a.requireRecovery(id, errors.New("crash after swap"))
	done()
	w := httptest.NewRecorder()
	a.httpRecoverOperation(w, httptest.NewRequest("POST", "/tenants/"+id+"/recover-operation", strings.NewReader(`{"confirm":true}`)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(tenant.ConfigDir, "data"))
	if err != nil || string(data) != "before" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	got, err := a.store.backupMetadata(id)
	if err != nil || !bytes.Equal(got.APIKey, m.APIKey) {
		t.Fatal("credentials did not roll back")
	}
	failed, _ := filepath.Glob(tenant.ConfigDir + ".failedrestore-*")
	if len(failed) != 1 {
		t.Fatal("failed payload was not retained")
	}
}

func TestSQLHostnameOwnershipRejectsCrossPurposeAndWildcard(t *testing.T) {
	a, _ := newTestApp(t)
	one := seedTenant(t, a, "owner-one", StatusStopped)
	two := seedTenant(t, a, "owner-two", StatusStopped)
	if _, err := a.claimHostname(one, "client.example.com", "grant", true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"client.example.com", "app.client.example.com"} {
		if _, err := a.store.db.Exec(`INSERT INTO fleet_hostname_owners(hostname,tenant_id,purpose) VALUES(?,?,'primary')`, name, two); err == nil {
			t.Fatalf("SQL allowed conflicting hostname %s", name)
		}
	}
	if _, err := a.claimHostname(two, "other.example.com", "primary", false); err != nil {
		t.Fatal(err)
	}
}

func TestPortReservationsIncludeLegacyAndApplicationBlocks(t *testing.T) {
	a, _ := newTestApp(t)
	one := seedTenant(t, a, "port-one", StatusStopped)
	two := seedTenant(t, a, "port-two", StatusStopped)
	tn, _, _ := a.store.get(one)
	if _, err := a.store.db.Exec(`DELETE FROM fleet_port_reservations WHERE tenant_id=?`, one); err != nil {
		t.Fatal(err)
	}
	other, _, _ := a.store.get(two)
	other.BaseURL = tn.BaseURL
	if err := a.store.reserveManagementPort(other); err == nil {
		t.Fatal("legacy management port reused")
	}
	base, err := a.reserveAppPortBlock(one, 0, portFromTenant(tn))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.db.Exec(`INSERT INTO fleet_port_reservations(instance_id,port,tenant_id,purpose) VALUES(0,?,?,'management')`, base, two); err == nil {
		t.Fatal("SQL allowed app/management overlap")
	}
}

func TestHealthProbeRechecksStoppedState(t *testing.T) {
	a, ctx := newTestApp(t)
	id := seedTenant(t, a, "stale-health", StatusActive)
	stale, _, _ := a.store.get(id)
	if err := a.store.setStatus(id, StatusStopped, "test"); err != nil {
		t.Fatal(err)
	}
	a.probeOnce(context.Background(), ctx, stale)
	got, _, _ := a.store.get(id)
	if got.Status != StatusStopped || got.RespawnAttempts != 0 {
		t.Fatalf("stale health changed state: %+v", got)
	}
}

func TestKeyedLocksAllowIndependentWorkAndCancellation(t *testing.T) {
	done, err := lockResource(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	second, err := lockResource(context.Background(), "second")
	if err != nil {
		t.Fatal(err)
	}
	second()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err = lockResource(ctx, "first"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked lock did not cancel: %v", err)
	}
}

func TestRemoteExtractorRejectsTraversalAndKeepsExistingDirectory(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	root := t.TempDir()
	archive := filepath.Join(root, "payload.tgz")
	if err := os.WriteFile(archive, auditArchive(t, ""), 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "tenant")
	cmd := exec.Command("sh", "-c", remoteExtractCommand(archive, destination))
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("unsafe archive accepted: %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatal("archive escaped")
	}
	if err := os.MkdirAll(destination, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(destination, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("sh", "-c", remoteExtractCommand(archive, destination)).Run(); err == nil {
		t.Fatal("existing destination overwritten")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatal("existing data modified")
	}
}

func TestLogRotationKeepsInheritedDescriptorAndBoundsRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet-child.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i := 0; i < 7; i++ {
		if err = f.Truncate(tenantLogLimit + 1); err != nil {
			t.Fatal(err)
		}
		if err = rotateTenantLog(path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.WriteString("still alive"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "still alive" {
		t.Fatalf("descriptor disconnected: %q %v", data, err)
	}
	files, _ := filepath.Glob(path + ".*")
	if len(files) != 5 {
		t.Fatalf("retained logs=%d", len(files))
	}
	for _, file := range files {
		info, _ := os.Stat(file)
		if info.Size() > tenantLogLimit {
			t.Fatal("rotation exceeded size limit")
		}
	}
}

func TestTenantListPaginationAndSearch(t *testing.T) {
	a, _ := newTestApp(t)
	for i := 0; i < 5; i++ {
		seedTenant(t, a, fmt.Sprintf("page-%d", i), StatusStopped)
	}
	read := func(path string) struct {
		Tenants []*Tenant `json:"tenants"`
		HasMore bool      `json:"has_more"`
	} {
		w := httptest.NewRecorder()
		a.httpList(w, httptest.NewRequest("GET", path, nil))
		var result struct {
			Tenants []*Tenant `json:"tenants"`
			HasMore bool      `json:"has_more"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := read("/tenants?limit=2")
	last := read("/tenants?limit=2&offset=4")
	if len(first.Tenants) != 2 || !first.HasMore || len(last.Tenants) != 1 || last.HasMore {
		t.Fatal("incorrect pagination")
	}
	found := read("/tenants?search=page-3")
	if len(found.Tenants) != 1 || found.Tenants[0].Slug != "page-3" {
		t.Fatal("search mismatch")
	}
}

func TestNewCloneIsBornLockedAndQuarantined(t *testing.T) {
	a, _ := newTestApp(t)
	enc, err := a.keys.seal([]byte("key"))
	if err != nil {
		t.Fatal(err)
	}
	tn := &Tenant{Slug: "new-clone", Kind: KindLocal, BaseURL: "http://localhost:65534", ConfigDir: filepath.Join(localDataRoot(), "new-clone"), OwnerEmail: "owner@example.com", Status: StatusStopped}
	done, err := a.insertTenantForOperation(tn, enc, nil, "clone destination", true)
	if err != nil {
		t.Fatal(err)
	}
	other := &App{store: a.store, operations: map[string]string{}}
	if _, err := other.beginTenantOperation(tn.ID, "start"); err == nil {
		t.Fatal("new clone visible without durable operation")
	}
	if required, err := a.cloneRequiresQuarantine(tn.ID); err != nil || !required {
		t.Fatalf("new clone lost quarantine: %v %v", required, err)
	}
	done()
	if required, err := a.cloneRequiresQuarantine(tn.ID); err != nil || !required {
		t.Fatal("finishing copy lifted quarantine")
	}
}

func TestRetentionNeverOffersPinnedVersions(t *testing.T) {
	a, _ := newTestApp(t)
	id := seedTenant(t, a, "pinned", StatusStopped)
	root := t.TempDir()
	t.Setenv("FLEET_VERSIONS_ROOT", root)
	// Use the actual configurable cache root recognized by versionsRoot.
	dir := versionsRoot()
	if dir != root {
		t.Skip("versions root has a different environment contract")
	}
	for _, version := range []string{"0.40.0", "0.41.0"} {
		p := filepath.Join(dir, version)
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-31 * 24 * time.Hour)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.store.setCurrentVersion(id, "0.41.0"); err != nil {
		t.Fatal(err)
	}
	plan, err := a.retentionPlan()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || filepath.Base(plan[0].Path) != "0.40.0" {
		t.Fatalf("retention offered active runtime: %+v", plan)
	}
}

func TestRemoteExtractorRoundTripsSafeSymlink(t *testing.T) {
	t.Setenv("FLEET_DATA_ROOT", t.TempDir())
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("value"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("data", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := streamTenantArchiveLocal(&archive, source); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "archive.tgz")
	if err := os.WriteFile(path, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if out, err := exec.Command("sh", "-c", remoteExtractCommand(path, target)).CombinedOutput(); err != nil {
		t.Fatalf("extract: %v %s", err, out)
	}
	if link, err := os.Readlink(filepath.Join(target, "link")); err != nil || link != "data" {
		t.Fatalf("symlink=%q err=%v", link, err)
	}
}

func TestCorruptGzipNeverPublishesTenant(t *testing.T) {
	t.Setenv("FLEET_DATA_ROOT", t.TempDir())
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("value"), 0600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := streamTenantArchiveLocal(&archive, source); err != nil {
		t.Fatal(err)
	}
	data := archive.Bytes()
	data[len(data)-8] ^= 0xff
	target := filepath.Join(t.TempDir(), "target")
	if err := extractTenantArchiveStream(bytes.NewReader(data), target); err == nil {
		t.Fatal("corrupt checksum accepted")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("corrupt archive published")
	}
}
