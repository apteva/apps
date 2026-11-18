package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	_ "modernc.org/sqlite"
)

func TestMain(m *testing.M) {
	// Runs execute synchronously so tests can assert on their outcome.
	kickOperation = func(a *App, ctx *sdk.AppCtx, id string) { a.drive(ctx, id) }
	now = func() time.Time { return time.Now().UTC().Add(time.Duration(clockOffset.Load())) }
	os.Exit(m.Run())
}

// ─── Fake platform: Containers + Instances ───────────────────────────────

type fakeInstance struct {
	instanceInfo
}

type platformStub struct {
	tk.BasePlatformClient
	mu            sync.Mutex
	calls         []callAppCall
	commands      []string
	ingress       []sdk.IngressExposeRequest
	unexposed     []string
	instances     map[int64]*fakeInstance
	nextInstance  int64
	failStep      string // access/reset step that exits non-zero
	pendingClean  int    // verify_clean polls that report "not yet"
	createErr     error
	destroyCalled []int64
	osVersion     string            // sw_vers / os-release answer
	onCall        map[string]func() // hooks run before a tool, outside the lock
	runErrStarted bool              // containers_run fails after starting the container
	workloads     []map[string]any  // what containers_list returns
}

type callAppCall struct {
	App   string
	Tool  string
	Input map[string]any
}

func newStub() *platformStub {
	return &platformStub{instances: map[int64]*fakeInstance{}, nextInstance: 500}
}

func (p *platformStub) addInstance(id int64, platform, status string) {
	p.instances[id] = &fakeInstance{instanceInfo{ID: id, Name: fmt.Sprintf("host-%d", id), Status: status, Platform: platform, PublicIPv4: fmt.Sprintf("10.0.0.%d", id%250), SSHPort: 22}}
}

var opIDRE = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}`)

var allMarkerSteps = []string{"prepare", "preflight", "grant", "verify_access", "revoke", "verify_revoked", "check_clean", "reboot",
	"wipe", "reinstall", "verify_clean", "install_wipe", "install_reinstall", "install_verify-clean"}

func (p *platformStub) CallAppResult(appName, tool string, input map[string]any, out any) error {
	if hook := p.onCall[tool]; hook != nil {
		hook()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, callAppCall{App: appName, Tool: tool, Input: input})
	reply := func(v any) error {
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out)
	}
	switch tool {
	case "containers_list":
		return reply(map[string]any{"workloads": p.workloads})
	case "containers_run":
		w := map[string]any{"id": "wl_" + strFromAny(input["name"]), "name": input["name"], "status": "running", "health_status": "healthy",
			"ports": []map[string]any{{"container_port": 80, "host_port": 18080, "bind_addr": "127.0.0.1"}}}
		p.workloads = append(p.workloads, w)
		if p.runErrStarted {
			return errors.New("containers_run: context deadline exceeded")
		}
		return reply(map[string]any{"workload": map[string]any{"id": "wl_" + strFromAny(input["name"]), "status": "running", "health_status": "healthy",
			"ports": []map[string]any{{"container_port": 80, "host_port": 18080, "bind_addr": "127.0.0.1"}}}})
	case "containers_stop", "containers_start", "containers_restart", "containers_health":
		return reply(map[string]any{"workload": map[string]any{"id": input["workload_id"], "status": "running", "health_status": "healthy"}})
	case "containers_destroy":
		return reply(map[string]any{"destroyed": true})
	case "containers_logs":
		return reply(map[string]any{"logs": "line"})
	case "containers_usage_get":
		return reply(map[string]any{"usage": map[string]any{"storage_mb": 12}})
	case "instance_get":
		inst, ok := p.instances[int64Arg(input, "id")]
		if !ok {
			return errors.New("instance not found")
		}
		return reply(map[string]any{"instance": inst.instanceInfo})
	case "instance_list":
		var list []instanceInfo
		for _, i := range p.instances {
			list = append(list, i.instanceInfo)
		}
		return reply(map[string]any{"instances": list})
	case "instance_create":
		if p.createErr != nil {
			return p.createErr
		}
		p.nextInstance++
		id := p.nextInstance
		platform := "linux"
		if strings.HasPrefix(strFromAny(input["size"]), "apple-silicon/") {
			platform = "macos"
		}
		p.instances[id] = &fakeInstance{instanceInfo{ID: id, Name: strFromAny(input["name"]), Status: "provisioning", Platform: platform, PublicIPv4: "203.0.113.9", SSHPort: 22}}
		return reply(map[string]any{"instance": p.instances[id].instanceInfo})
	case "instance_destroy":
		id := int64Arg(input, "id")
		p.destroyCalled = append(p.destroyCalled, id)
		if inst, ok := p.instances[id]; ok {
			inst.Status = "destroyed"
		}
		return reply(map[string]any{"destroyed": true, "id": id})
	case "instance_upload_file":
		return reply(map[string]any{"ok": true})
	case "instance_metrics":
		return reply(map[string]any{"cpu_percent": 3})
	case "instance_run_command":
		cmd := strFromAny(input["cmd"])
		p.commands = append(p.commands, cmd)
		op := opIDRE.FindString(cmd)
		if strings.Contains(cmd, "sw_vers") || strings.Contains(cmd, "os-release") {
			return reply(map[string]any{"output": firstNonEmpty(p.osVersion, "15.3") + "\n", "exit_code": 0})
		}
		if p.failStep != "" && strings.Contains(cmd, "'"+p.failStep+"'") {
			return reply(map[string]any{"output": "boom", "exit_code": 1})
		}
		if p.pendingClean > 0 && strings.Contains(cmd, "/verify-clean") {
			p.pendingClean--
			return reply(map[string]any{"output": "", "exit_code": 17})
		}
		var b strings.Builder
		for _, s := range allMarkerSteps {
			b.WriteString(marker(op, s) + "\n")
		}
		return reply(map[string]any{"output": b.String(), "exit_code": 0})
	}
	return fmt.Errorf("unexpected tool %s/%s", appName, tool)
}

func (p *platformStub) ExposeIngress(req sdk.IngressExposeRequest) (*sdk.IngressRoute, error) {
	p.ingress = append(p.ingress, req)
	return &sdk.IngressRoute{ID: 1, Hostname: req.Hostname, Target: req.Target, Status: "active"}, nil
}

func (p *platformStub) UnexposeIngress(hostname string) error {
	p.unexposed = append(p.unexposed, hostname)
	return nil
}

func (p *platformStub) called(tool string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.calls {
		if c.Tool == tool {
			n++
		}
	}
	return n
}

func (p *platformStub) lastCall(tool string) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.calls) - 1; i >= 0; i-- {
		if p.calls[i].Tool == tool {
			return p.calls[i].Input
		}
	}
	return nil
}

// ─── Harness ─────────────────────────────────────────────────────────────

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "hosting.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func applyMigrations(t *testing.T, db *sql.DB, upTo string) {
	t.Helper()
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	for _, f := range files {
		if upTo != "" && filepath.Base(f) > upTo {
			break
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
}

func newTestCtx(t *testing.T, pf sdk.PlatformClient) (*App, *sdk.AppCtx, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	applyMigrations(t, db, "")
	app := &App{}
	manifest := app.Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{
		"default_base_domain": "hosted.example.com",
		"default_image":       "apteva:test",
	}, pf, nil)
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	return app, ctx, db
}

func call(t *testing.T, fn func(*sdk.AppCtx, map[string]any) (any, error), ctx *sdk.AppCtx, args map[string]any) map[string]any {
	t.Helper()
	out, err := fn(ctx, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(out)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func callErr(t *testing.T, fn func(*sdk.AppCtx, map[string]any) (any, error), ctx *sdk.AppCtx, args map[string]any, want string) {
	t.Helper()
	_, err := fn(ctx, args)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want containing %q", err, want)
	}
}

func tenant(t *testing.T, db *sql.DB, id string) *Tenant {
	t.Helper()
	tn, err := dbTenantGet(db, id)
	if err != nil || tn == nil {
		t.Fatalf("tenant %s: %v", id, err)
	}
	return tn
}

func machine(t *testing.T, db *sql.DB, id int64) *Machine {
	t.Helper()
	m, err := dbMachineGet(db, id)
	if err != nil || m == nil {
		t.Fatalf("machine %d: %v", id, err)
	}
	return m
}

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGq0jXv1vYw3wCw5JpQ8 customer@laptop"

// setupMacPool creates a validated, inspected macOS pool with n machines and
// a machine_pool profile "mac-m4".
func setupMacPool(t *testing.T, a *App, ctx *sdk.AppCtx, stub *platformStub, n int) []int64 {
	t.Helper()
	call(t, a.toolPoolUpsert, ctx, map[string]any{"key": "macs", "name": "Macs", "platform": "macos"})
	call(t, a.toolPoolSetExecution, ctx, map[string]any{"key": "macs", "enabled": true})
	call(t, a.toolProfileUpsert, ctx, map[string]any{"key": "mac-m4", "backend": "machine_pool", "product_key": "ssh-access", "pool_key": "macs"})
	var ids []int64
	for i := 0; i < n; i++ {
		instID := int64(100 + i)
		stub.addInstance(instID, "macos", "ready")
		out := call(t, a.toolMachineAdd, ctx, map[string]any{"pool_key": "macs", "instance_id": instID, "serial": fmt.Sprintf("SER%d", i), "management_user": "admin"})
		id := int64Arg(mapFromAny(out["machine"]), "id")
		call(t, a.toolMachineUpdate, ctx, map[string]any{"machine_id": id, "validated": true, "confirm": fmt.Sprint(id)})
		call(t, a.toolMachineInspect, ctx, map[string]any{"machine_id": id})
		if m := machine(t, ctx.AppDB(), id); m.State != MachineAvailable {
			t.Fatalf("machine %d after inspect = %s (%s)", id, m.State, m.StateReason)
		}
		ids = append(ids, id)
	}
	return ids
}

// ─── Manifest and schema ─────────────────────────────────────────────────

func TestEmbeddedManifestValid(t *testing.T) {
	m := (&App{}).Manifest()
	if m.Name != "hosting" || m.Version != appVersion {
		t.Fatalf("manifest name/version = %s/%s, want hosting/%s", m.Name, m.Version, appVersion)
	}
	for _, dep := range m.Requires.Apps {
		if !dep.Optional {
			t.Fatalf("dependency %s must be optional", dep.Name)
		}
		if dep.Name != "containers" && dep.Name != "instances" {
			t.Fatalf("unexpected dependency %s", dep.Name)
		}
		if len(dep.Events) > 0 {
			t.Fatalf("hosting must not subscribe to %s events", dep.Name)
		}
	}
	if len((&App{}).EventHandlers()) != 0 {
		t.Fatal("hosting must not handle events")
	}
}

func TestMCPToolsMatchManifest(t *testing.T) {
	a := &App{}
	m := a.Manifest()
	declared := map[string]bool{}
	for _, tool := range m.Provides.MCPTools {
		declared[tool.Name] = true
	}
	handlers := map[string]bool{}
	for _, tool := range a.MCPTools() {
		if tool.Handler == nil {
			t.Fatalf("%s has no handler", tool.Name)
		}
		handlers[tool.Name] = true
		if !declared[tool.Name] {
			t.Fatalf("%s is not declared in apteva.yaml", tool.Name)
		}
	}
	for name := range declared {
		if !handlers[name] {
			t.Fatalf("%s declared but has no handler", name)
		}
	}
	for _, gone := range []string{"hosting_checkout_create", "hosting_paid_invoice_provision", "hosting_subscription_sync", "hosting_addon_enable", "hosting_metered_invoice_create"} {
		if handlers[gone] {
			t.Fatalf("%s should be removed in 2.0", gone)
		}
	}
}

func TestMigrationKeepsLegacyTenants(t *testing.T) {
	db := openTestDB(t)
	applyMigrations(t, db, "003_addons_metering.sql")
	mustExec := func(q string, args ...any) {
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO hosting_products (key, name) VALUES ('apteva', 'Apteva')`)
	mustExec(`INSERT INTO hosting_plans (key, name, image, cpu, memory_mb, storage_mb) VALUES ('starter', 'Starter', 'img', 1, 1024, 2048)`)
	mustExec(`INSERT INTO hosting_plan_bindings (plan_key, product_key) VALUES ('starter', 'apteva')`)
	mustExec(`INSERT INTO hosting_customers (id, email) VALUES (1, 'a@example.com')`)
	mustExec(`INSERT INTO hosting_tenants (id, customer_id, workload_id, slug, default_hostname, owner_email, plan_key, status) VALUES ('htn_live', 1, 'wl1', 'live', 'live.example.com', 'a@example.com', 'starter', 'active')`)
	mustExec(`INSERT INTO hosting_tenants (id, customer_id, workload_id, slug, default_hostname, owner_email, plan_key, status) VALUES ('htn_old', 1, 'wl2', 'old', 'old.example.com', 'a@example.com', 'starter', 'deleted')`)
	mustExec(`INSERT INTO hosting_tenant_addons (tenant_id, customer_id, addon_key, feature_key) VALUES ('htn_live', 1, 'llm', 'llm.tokens')`)
	applyMigrations(t, db, "")

	live, err := dbTenantGet(db, "htn_live")
	if err != nil || live == nil {
		t.Fatalf("legacy tenant lost: %v", err)
	}
	if live.ProfileKey != "starter" || live.Backend != BackendContainer || live.Status != TenantActive || live.DefaultHostname != "live.example.com" {
		t.Fatalf("legacy tenant = %+v", live)
	}
	old, _ := dbTenantGet(db, "htn_old")
	if old.Status != TenantReleased || old.ReleasedAt == "" {
		t.Fatalf("deleted tenant should map to released, got %s", old.Status)
	}
	p, _ := dbProfileGet(db, "starter")
	if p == nil || p.Backend != BackendContainer || p.ProductKey != "apteva" || p.StorageMB != 2048 {
		t.Fatalf("plan not migrated to profile: %+v", p)
	}
	var fkProblems int
	rows, _ := db.Query(`PRAGMA foreign_key_check`)
	for rows.Next() {
		fkProblems++
	}
	rows.Close()
	if fkProblems != 0 {
		t.Fatalf("foreign key check reported %d problems", fkProblems)
	}
}

func TestScriptsAreEmbeddedAndParse(t *testing.T) {
	for _, platform := range []string{"macos", "linux"} {
		b, err := accessScript(platform)
		if err != nil || len(b) == 0 {
			t.Fatalf("%s access script: %v", platform, err)
		}
	}
	hashes := shippedResetHashes()
	for _, name := range []string{"wipe", "reinstall", "verify-clean"} {
		if len(hashes[name]) != 64 {
			t.Fatalf("missing hash for %s", name)
		}
	}
	files, _ := filepath.Glob("scripts/*/*.sh")
	for _, f := range files {
		if out, err := exec.Command("/bin/sh", "-n", f).CombinedOutput(); err != nil {
			t.Fatalf("%s does not parse: %v %s", f, err, out)
		}
		raw, _ := os.ReadFile(f)
		if strings.Contains(strings.ToLower(string(raw)), "my-cloud-mac") || strings.Contains(string(raw), "mcm_") {
			t.Fatalf("%s still uses My Cloud Mac names", f)
		}
	}
	if id := newOperationID(); !regexp.MustCompile(`^[a-f0-9-]{36}$`).MatchString(id) {
		t.Fatalf("operation id %q does not satisfy the reset scripts", id)
	}
	if u := customerUser("x"); !regexp.MustCompile(`^hst_[a-f0-9]{20}$`).MatchString(u) {
		t.Fatalf("customer user %q does not satisfy the access script", u)
	}
}

// ─── Container backend ───────────────────────────────────────────────────

func TestContainerTenantLifecycleIsIdempotent(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	args := map[string]any{"external_ref": "acct-1", "profile": "wordpress-starter", "owner_email": "Owner@Example.com", "slug": "blog"}
	out := call(t, a.toolTenantCreate, ctx, args)
	tn := mapFromAny(out["tenant"])
	id := strArg(tn, "id")
	if strArg(tn, "status") != TenantActive || strArg(tn, "default_hostname") != "blog.hosted.example.com" || strArg(tn, "owner_email") != "owner@example.com" {
		t.Fatalf("tenant = %v", tn)
	}
	if len(stub.ingress) != 1 || stub.ingress[0].Target != "http://127.0.0.1:18080" {
		t.Fatalf("ingress = %+v", stub.ingress)
	}
	if run := stub.lastCall("containers_run"); strFromAny(run["image"]) != "wordpress:php8.3-apache" {
		t.Fatalf("containers_run image = %v", run["image"])
	}

	again := call(t, a.toolTenantCreate, ctx, args)
	if strArg(mapFromAny(again["tenant"]), "id") != id || stub.called("containers_run") != 1 {
		t.Fatal("repeat create must return the same tenant without a second container")
	}
	callErr(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-1", "profile": "starter", "owner_email": "o@example.com"}, "already has tenant")

	call(t, a.toolTenantSuspend, ctx, map[string]any{"external_ref": "acct-1"})
	call(t, a.toolTenantSuspend, ctx, map[string]any{"external_ref": "acct-1"})
	if tenant(t, db, id).Status != TenantSuspended || stub.called("containers_stop") != 1 {
		t.Fatalf("suspend not idempotent: stops=%d", stub.called("containers_stop"))
	}
	call(t, a.toolTenantResume, ctx, map[string]any{"external_ref": "acct-1"})
	if tenant(t, db, id).Status != TenantActive || stub.called("containers_start") != 1 {
		t.Fatal("resume did not start the container")
	}
	call(t, a.toolTenantRelease, ctx, map[string]any{"external_ref": "acct-1", "confirm": id})
	rel := call(t, a.toolTenantRelease, ctx, map[string]any{"external_ref": "acct-1", "confirm": id})
	if rel["already_released"] != true || stub.called("containers_destroy") != 1 || len(stub.unexposed) != 1 {
		t.Fatalf("release not idempotent: %v destroys=%d", rel, stub.called("containers_destroy"))
	}
	released := tenant(t, db, id)
	if released.Status != TenantReleased || released.DefaultHostname != "" {
		t.Fatalf("released tenant = %+v", released)
	}
	// The same account can come back, and reuse the slug.
	back := call(t, a.toolTenantCreate, ctx, args)
	if strArg(mapFromAny(back["tenant"]), "id") == id {
		t.Fatal("a released tenant must not be reused")
	}
}

func TestContainerProfileRejectsMachineProduct(t *testing.T) {
	a, ctx, _ := newTestCtx(t, newStub())
	callErr(t, a.toolProfileUpsert, ctx, map[string]any{"key": "bad", "backend": "container", "product_key": "ssh-access"}, "needs a machine backend")
	callErr(t, a.toolProfileUpsert, ctx, map[string]any{"key": "bad", "backend": "machine_pool", "product_key": "ssh-access"}, "pool_key")
	callErr(t, a.toolProfileUpsert, ctx, map[string]any{"key": "bad", "backend": "machine_on_demand", "product_key": "ssh-access", "settings": map[string]any{"provider": "hetzner"}}, "server_type")
	callErr(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "x", "profile": "nope", "owner_email": "o@example.com"}, "not found")
	callErr(t, a.toolTenantCreate, ctx, map[string]any{"profile": "starter", "owner_email": "o@example.com"}, "external_ref required")
	callErr(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "x", "profile": "starter", "owner_email": "o@example.com", "ssh_keys": []any{"not a key"}}, "not an SSH public key")
}

// ─── Pool backend ────────────────────────────────────────────────────────

func TestPoolMachineFullCycle(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupMacPool(t, a, ctx, stub, 1)
	mid := ids[0]

	av := call(t, a.toolAvailability, ctx, map[string]any{"profile": "mac-m4"})
	if mapFromAny(av["availability"])["for_sale"] != true {
		t.Fatalf("availability = %v", av)
	}
	res := call(t, a.toolReservationCreate, ctx, map[string]any{"profile": "mac-m4", "external_ref": "acct-9"})
	rid := strArg(mapFromAny(res["reservation"]), "id")
	if again := call(t, a.toolReservationCreate, ctx, map[string]any{"profile": "mac-m4", "external_ref": "acct-9"}); strArg(mapFromAny(again["reservation"]), "id") != rid {
		t.Fatal("repeat reservation must return the same hold")
	}
	if machine(t, db, mid).State != MachineReserved {
		t.Fatal("machine should be reserved")
	}
	callErr(t, a.toolReservationCreate, ctx, map[string]any{"profile": "mac-m4", "external_ref": "acct-other"}, "no machine available")

	out := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-9", "profile": "mac-m4", "owner_email": "c@example.com", "reservation_id": rid, "ssh_keys": []any{testKey}})
	tn := tenant(t, db, strArg(mapFromAny(out["tenant"]), "id"))
	if tn.Status != TenantActive || tn.MachineID != mid {
		t.Fatalf("tenant = %+v", tn)
	}
	if tn.Access == nil || !tn.Access.Granted || tn.Access.Command != fmt.Sprintf("ssh %s@10.0.0.100 -p 22", tn.SSHUser) {
		t.Fatalf("access = %+v", tn.Access)
	}
	if m := machine(t, db, mid); m.State != MachineAssigned || m.TenantID != tn.ID {
		t.Fatalf("machine = %s tenant=%s", m.State, m.TenantID)
	}
	if r, _ := dbReservationGet(db, rid); r.Status != "confirmed" || r.TenantID != tn.ID {
		t.Fatalf("reservation = %+v", r)
	}
	// Every command goes through the hash check as root.
	for _, c := range stub.commands {
		if !strings.Contains(c, "sudo -n /bin/sh -c") {
			t.Fatalf("command not run as root: %s", c)
		}
	}

	call(t, a.toolTenantSuspend, ctx, map[string]any{"external_ref": "acct-9"})
	if tenant(t, db, tn.ID).Status != TenantSuspended || machine(t, db, mid).State != MachineSuspended {
		t.Fatal("suspend did not revoke access")
	}
	if tenant(t, db, tn.ID).Access.Granted {
		t.Fatal("suspended tenant must not report access")
	}
	call(t, a.toolTenantResume, ctx, map[string]any{"external_ref": "acct-9"})
	if tenant(t, db, tn.ID).Status != TenantActive || machine(t, db, mid).State != MachineAssigned {
		t.Fatal("resume did not restore access")
	}

	before := len(stub.commands)
	call(t, a.toolTenantRelease, ctx, map[string]any{"external_ref": "acct-9", "confirm": tn.ID})
	if got := tenant(t, db, tn.ID); got.Status != TenantReleased {
		t.Fatalf("tenant after release = %s", got.Status)
	}
	m := machine(t, db, mid)
	if m.State != MachineAvailable || m.TenantID != "" {
		t.Fatalf("machine after release = %s tenant=%q reason=%q", m.State, m.TenantID, m.StateReason)
	}
	joined := strings.Join(stub.commands[before:], "\n")
	for _, want := range []string{"'revoke'", "'verify_revoked'", "/usr/local/libexec/apteva-hosting/wipe", "/usr/local/libexec/apteva-hosting/verify-clean", "'prepare'", "'check_clean'"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("release did not run %s", want)
		}
	}
	// The account is free to buy again.
	if again := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-9", "profile": "mac-m4", "owner_email": "c@example.com"}); strArg(mapFromAny(again["tenant"]), "id") == tn.ID {
		t.Fatal("released tenant reused")
	}
}

func TestPoolTenantWaitsForMachine(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	setupMacPool(t, a, ctx, stub, 0)
	out := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-w", "profile": "mac-m4", "owner_email": "w@example.com", "ssh_keys": testKey})
	id := strArg(mapFromAny(out["tenant"]), "id")
	if tenant(t, db, id).Status != TenantWaitingForMachine {
		t.Fatalf("status = %s", tenant(t, db, id).Status)
	}
	av := call(t, a.toolAvailability, ctx, map[string]any{"profile": "mac-m4"})
	if mapFromAny(av["availability"])["for_sale"] != false {
		t.Fatal("empty pool must not be for sale")
	}
	// A machine arrives: the waiting tenant gets it once inspect passes.
	stub.addInstance(300, "macos", "ready")
	m := call(t, a.toolMachineAdd, ctx, map[string]any{"pool_key": "macs", "instance_id": 300, "serial": "S300", "management_user": "admin"})
	mid := int64Arg(mapFromAny(m["machine"]), "id")
	call(t, a.toolMachineUpdate, ctx, map[string]any{"machine_id": mid, "validated": true, "confirm": fmt.Sprint(mid)})
	call(t, a.toolMachineInspect, ctx, map[string]any{"machine_id": mid})
	got := tenant(t, db, id)
	if got.Status != TenantActive || got.MachineID != mid {
		t.Fatalf("waiting tenant not assigned: %+v", got)
	}
}

func TestUnvalidatedMachineIsNeverSold(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	setupMacPool(t, a, ctx, stub, 0)
	stub.addInstance(301, "macos", "ready")
	m := call(t, a.toolMachineAdd, ctx, map[string]any{"pool_key": "macs", "instance_id": 301, "serial": "S", "management_user": "admin"})
	mid := int64Arg(mapFromAny(m["machine"]), "id")
	call(t, a.toolMachineInspect, ctx, map[string]any{"machine_id": mid})
	got := machine(t, db, mid)
	if got.State != MachineQuarantined || !strings.Contains(got.StateReason, "validated") {
		t.Fatalf("unvalidated machine = %s (%s)", got.State, got.StateReason)
	}
	if len(got.ScriptHashes) != 3 {
		t.Fatalf("inspect should pin reset script hashes, got %v", got.ScriptHashes)
	}
	callErr(t, a.toolMachineClean, ctx, map[string]any{"machine_id": mid, "confirm": fmt.Sprint(mid)}, "not validated")
	callErr(t, a.toolMachineUpdate, ctx, map[string]any{"machine_id": mid, "validated": true, "confirm": "nope"}, "confirm must be")
	callErr(t, a.toolMachineAdd, ctx, map[string]any{"pool_key": "macs", "instance_id": 301}, "already a machine")
}

func TestExecutionSwitchHoldsRunsWithoutQuarantine(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupMacPool(t, a, ctx, stub, 1)
	out := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-x", "profile": "mac-m4", "owner_email": "x@example.com", "ssh_keys": testKey})
	id := strArg(mapFromAny(out["tenant"]), "id")
	call(t, a.toolPoolSetExecution, ctx, map[string]any{"key": "macs", "enabled": false})
	before := len(stub.commands)
	call(t, a.toolTenantSuspend, ctx, map[string]any{"tenant_id": id})
	if len(stub.commands) != before {
		t.Fatal("a command ran while execution was off")
	}
	op, _ := activeOperationFor(db, id, 0)
	if op == nil || op.Status != OpWaiting {
		t.Fatalf("run should wait, got %+v", op)
	}
	if machine(t, db, ids[0]).State == MachineQuarantined {
		t.Fatal("switching execution off must not quarantine")
	}
	call(t, a.toolPoolSetExecution, ctx, map[string]any{"key": "macs", "enabled": true})
	advanceClock(t, time.Minute)
	if err := a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	if tenant(t, db, id).Status != TenantSuspended {
		t.Fatalf("suspend did not finish after execution came back: %s", tenant(t, db, id).Status)
	}
}

func TestFailedStepQuarantinesAndNeedsReviewedRetry(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupMacPool(t, a, ctx, stub, 1)
	stub.failStep = "grant"
	out := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-f", "profile": "mac-m4", "owner_email": "f@example.com", "ssh_keys": testKey})
	id := strArg(mapFromAny(out["tenant"]), "id")
	if tenant(t, db, id).Status != TenantFailed {
		t.Fatalf("tenant = %s", tenant(t, db, id).Status)
	}
	m := machine(t, db, ids[0])
	if m.State != MachineQuarantined || !strings.Contains(m.StateReason, "grant") {
		t.Fatalf("machine = %s (%s)", m.State, m.StateReason)
	}
	ops, _ := dbOperationList(db, map[string]any{"tenant_id": id})
	opID := ops[0].ID
	callErr(t, a.toolOperationRetry, ctx, map[string]any{"operation_id": opID, "reviewed": false}, "reviewed=true")
	stub.failStep = ""
	call(t, a.toolOperationRetry, ctx, map[string]any{"operation_id": opID, "reviewed": true, "actor": "ops@example.com"})
	if tenant(t, db, id).Status != TenantActive || machine(t, db, ids[0]).State != MachineAssigned {
		t.Fatalf("retry did not finish: tenant=%s machine=%s", tenant(t, db, id).Status, machine(t, db, ids[0]).State)
	}
	op, _ := dbOperationGet(db, opID)
	if op.Attempts != 1 || op.Status != OpSucceeded {
		t.Fatalf("op = %+v", op)
	}
}

func TestVerifyCleanIsPolledUntilItPasses(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupMacPool(t, a, ctx, stub, 1)
	out := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-p", "profile": "mac-m4", "owner_email": "p@example.com"})
	id := strArg(mapFromAny(out["tenant"]), "id")
	stub.pendingClean = 2
	call(t, a.toolTenantRelease, ctx, map[string]any{"tenant_id": id, "confirm": id})
	if tenant(t, db, id).Status != TenantReleasing || machine(t, db, ids[0]).State != MachineCleaning {
		t.Fatal("release should wait for verify-clean")
	}
	for i := 0; i < 3; i++ {
		advanceClock(t, time.Minute)
		_ = a.tick(ctx)
	}
	if tenant(t, db, id).Status != TenantReleased || machine(t, db, ids[0]).State != MachineAvailable {
		t.Fatalf("release did not finish: %s / %s", tenant(t, db, id).Status, machine(t, db, ids[0]).State)
	}
	// Wipe ran once; only verification was repeated.
	wipes := 0
	for _, c := range stub.commands {
		if strings.Contains(c, "-wipe '") { // the pinned copy run with its arguments
			wipes++
		}
	}
	if wipes != 1 {
		t.Fatalf("wipe ran %d times", wipes)
	}
}

func TestVerifyCleanTimeoutQuarantines(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupMacPool(t, a, ctx, stub, 1)
	out := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-t", "profile": "mac-m4", "owner_email": "t@example.com"})
	id := strArg(mapFromAny(out["tenant"]), "id")
	stub.pendingClean = 1000
	call(t, a.toolTenantRelease, ctx, map[string]any{"tenant_id": id, "confirm": id})
	advanceClock(t, 61*time.Minute)
	_ = a.tick(ctx)
	if m := machine(t, db, ids[0]); m.State != MachineQuarantined || !strings.Contains(m.StateReason, "verify_clean") {
		t.Fatalf("machine = %s (%s)", m.State, m.StateReason)
	}
	if tenant(t, db, id).Status != TenantReleasing {
		t.Fatal("tenant should stay releasing until an operator resolves the machine")
	}
}

func TestReservationExpiresBackToSale(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupMacPool(t, a, ctx, stub, 1)
	res := call(t, a.toolReservationCreate, ctx, map[string]any{"profile": "mac-m4", "external_ref": "acct-r"})
	rid := strArg(mapFromAny(res["reservation"]), "id")
	advanceClock(t, 31*time.Minute)
	_ = a.tick(ctx)
	if r, _ := dbReservationGet(db, rid); r.Status != "expired" {
		t.Fatalf("reservation = %s", r.Status)
	}
	if machine(t, db, ids[0]).State != MachineAvailable {
		t.Fatal("expired hold must return the machine")
	}
	// A late payment with the expired hold still gets the machine if free.
	out := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-r", "profile": "mac-m4", "owner_email": "r@example.com", "reservation_id": rid})
	if tenant(t, db, strArg(mapFromAny(out["tenant"]), "id")).MachineID != ids[0] {
		t.Fatal("late tenant should take the free machine")
	}
	callErr(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-z", "profile": "mac-m4", "owner_email": "z@example.com", "reservation_id": rid}, "another profile or external_ref")
}

func TestQuarantineFencesRunsAndMaintenanceToggle(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupMacPool(t, a, ctx, stub, 1)
	mid := ids[0]
	call(t, a.toolMachineSetMaintenance, ctx, map[string]any{"machine_id": mid, "maintenance": true})
	if machine(t, db, mid).State != MachineMaintenance {
		t.Fatal("maintenance on")
	}
	call(t, a.toolMachineSetMaintenance, ctx, map[string]any{"machine_id": mid, "maintenance": false})
	if machine(t, db, mid).State != MachineAvailable {
		t.Fatal("maintenance off should return to available without inspect")
	}
	// A run held by the execution switch is stopped by quarantine.
	call(t, a.toolPoolSetExecution, ctx, map[string]any{"key": "macs", "enabled": false})
	_, err := enqueueOperation(db, OpInspect, "", mid, 0, []string{"preflight"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	call(t, a.toolMachineQuarantine, ctx, map[string]any{"machine_id": mid, "reason": "disk noise"})
	if op, _ := activeOperationFor(db, "", mid); op != nil {
		t.Fatalf("quarantine left run %s %s", op.ID, op.Status)
	}
	if m := machine(t, db, mid); m.State != MachineQuarantined || m.StateReason != "disk noise" {
		t.Fatalf("machine = %s (%s)", m.State, m.StateReason)
	}
	callErr(t, a.toolMachineRemove, ctx, map[string]any{"machine_id": mid, "confirm": "1x"}, "confirm must be")
	call(t, a.toolMachineRemove, ctx, map[string]any{"machine_id": mid, "confirm": fmt.Sprint(mid)})
	if m, _ := dbMachineGet(db, mid); m != nil {
		t.Fatal("machine not removed")
	}
}

func TestRunsForOneTenantKeepRequestOrder(t *testing.T) {
	stub := newStub()
	_, ctx, db := newTestCtx(t, stub)
	first, _ := enqueueOperation(db, OpSuspend, "htn_x", 0, 0, []string{"preflight"}, "t")
	second, _ := enqueueOperation(db, OpResume, "htn_x", 0, 0, []string{"preflight"}, "t")
	if op, _, err := claimOperation(ctx.AppDB(), second.ID); err != nil || op != nil {
		t.Fatalf("second run claimed before the first: %v %v", op, err)
	}
	if op, _, err := claimOperation(ctx.AppDB(), first.ID); err != nil || op == nil {
		t.Fatalf("first run not claimable: %v", err)
	}
}

func TestLinuxPoolReleaseWaitsForManualClean(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	call(t, a.toolPoolUpsert, ctx, map[string]any{"key": "boxes", "platform": "linux"})
	call(t, a.toolPoolSetExecution, ctx, map[string]any{"key": "boxes", "enabled": true})
	call(t, a.toolProfileUpsert, ctx, map[string]any{"key": "box", "backend": "machine_pool", "product_key": "ssh-access", "pool_key": "boxes"})
	stub.addInstance(400, "linux", "ready")
	m := call(t, a.toolMachineAdd, ctx, map[string]any{"pool_key": "boxes", "instance_id": 400})
	mid := int64Arg(mapFromAny(m["machine"]), "id")
	call(t, a.toolMachineUpdate, ctx, map[string]any{"machine_id": mid, "validated": true, "confirm": fmt.Sprint(mid)})
	call(t, a.toolMachineInspect, ctx, map[string]any{"machine_id": mid})
	out := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-l", "profile": "box", "owner_email": "l@example.com", "ssh_keys": testKey})
	id := strArg(mapFromAny(out["tenant"]), "id")
	call(t, a.toolTenantRelease, ctx, map[string]any{"tenant_id": id, "confirm": id})
	if tenant(t, db, id).Status != TenantReleased {
		t.Fatal("tenant should be released")
	}
	if got := machine(t, db, mid); got.State != MachineQuarantined || !strings.Contains(got.StateReason, "no automatic reset") {
		t.Fatalf("linux machine after release = %s (%s)", got.State, got.StateReason)
	}
	callErr(t, a.toolMachineClean, ctx, map[string]any{"machine_id": mid, "confirm": fmt.Sprint(mid)}, "only available for macOS")
	callErr(t, a.toolTenantReinstall, ctx, map[string]any{"external_ref": "acct-l"}, "no live tenant")
}

// ─── On-demand backend ───────────────────────────────────────────────────

func TestOnDemandMachineCreateAndDelayedDestroy(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	call(t, a.toolProfileUpsert, ctx, map[string]any{"key": "mac-ondemand", "backend": "machine_on_demand", "product_key": "ssh-access",
		"settings": map[string]any{"provider": "scaleway", "server_type": "apple-silicon/M4-S", "region": "fr-par-3", "platform": "macos"}})
	out := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-o", "profile": "mac-ondemand", "owner_email": "o@example.com", "ssh_keys": []any{testKey}})
	id := strArg(mapFromAny(out["tenant"]), "id")
	tn := tenant(t, db, id)
	if tn.Status != TenantProvisioning || tn.InstanceID == 0 {
		t.Fatalf("tenant after create = %+v", tn)
	}
	create := stub.lastCall("instance_create")
	if create["provider"] != "scaleway" || create["size"] != "apple-silicon/M4-S" || create["region"] != "fr-par-3" {
		t.Fatalf("instance_create input = %v", create)
	}
	// Instance becomes ready; the worker finishes provisioning.
	stub.instances[tn.InstanceID].Status = "ready"
	stub.instances[tn.InstanceID].DeletableAt = sqlTime(now().Add(24 * time.Hour))
	advanceClock(t, time.Minute)
	_ = a.tick(ctx)
	tn = tenant(t, db, id)
	if tn.Status != TenantActive || tn.Access == nil || tn.Access.Host != "203.0.113.9" {
		t.Fatalf("tenant after ready = %s access=%+v", tn.Status, tn.Access)
	}
	// Release inside the 24-hour minimum: access goes now, destroy waits.
	call(t, a.toolTenantRelease, ctx, map[string]any{"external_ref": "acct-o", "confirm": id})
	if len(stub.destroyCalled) != 0 || tenant(t, db, id).Status != TenantReleasing {
		t.Fatal("destroy must wait for deletable_at")
	}
	if strArg(mapFromAny(tenant(t, db, id).Metadata), "destroy_scheduled_at") == "" {
		t.Fatal("scheduled destroy time not recorded")
	}
	advanceClock(t, 25*time.Hour)
	_ = a.tick(ctx)
	if len(stub.destroyCalled) != 1 || tenant(t, db, id).Status != TenantReleased {
		t.Fatalf("destroy after deletable_at: calls=%v status=%s", stub.destroyCalled, tenant(t, db, id).Status)
	}
}

func TestOnDemandCreateAdoptsInstanceAfterLostLease(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	call(t, a.toolProfileUpsert, ctx, map[string]any{"key": "vps", "backend": "machine_on_demand", "product_key": "ssh-access", "settings": map[string]any{"provider": "hetzner", "server_type": "cx22"}})
	stub.createErr = errors.New("provider timeout")
	out := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-v", "profile": "vps", "owner_email": "v@example.com"})
	id := strArg(mapFromAny(out["tenant"]), "id")
	if tenant(t, db, id).Status != TenantFailed {
		t.Fatal("create error should fail the tenant")
	}
	// The provider did create it after all: retry adopts instead of buying twice.
	stub.createErr = nil
	stub.instances[900] = &fakeInstance{instanceInfo{ID: 900, Name: "hosting-" + id, Status: "ready", Platform: "linux", PublicIPv4: "198.51.100.4"}}
	ops, _ := dbOperationList(db, map[string]any{"tenant_id": id})
	call(t, a.toolOperationRetry, ctx, map[string]any{"operation_id": ops[0].ID, "reviewed": true})
	if stub.called("instance_create") != 1 {
		t.Fatalf("instance_create called %d times", stub.called("instance_create"))
	}
	if tn := tenant(t, db, id); tn.InstanceID != 900 || tn.Status != TenantActive {
		t.Fatalf("tenant = %+v", tn)
	}
}

// ─── HTTP ────────────────────────────────────────────────────────────────

func TestHTTPRoutesServePanelData(t *testing.T) {
	stub := newStub()
	a, ctx, _ := newTestCtx(t, stub)
	setupMacPool(t, a, ctx, stub, 1)
	call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-h", "profile": "starter", "owner_email": "h@example.com"})
	for _, path := range []string{"/overview", "/profiles", "/products", "/pools", "/machines", "/tenants", "/operations"} {
		var h http.HandlerFunc
		for _, r := range a.routes() {
			if r.Pattern == path {
				h = r.Handler
			}
		}
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s = %d", path, rec.Code)
		}
	}
}

// ─── Clock ───────────────────────────────────────────────────────────────

// clockOffset shifts the app clock in tests. It is atomic because a running
// step's lease heartbeat reads the clock from its own goroutine.
var clockOffset atomic.Int64

func advanceClock(t *testing.T, d time.Duration) {
	t.Helper()
	clockOffset.Add(int64(d))
	t.Cleanup(func() { clockOffset.Add(-int64(d)) })
}

// ─── Review fixes (2.1.0) ────────────────────────────────────────────────

func TestCreateAgainResumesSuspendedTenant(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	args := map[string]any{"external_ref": "acct-c", "profile": "starter", "owner_email": "c@example.com"}
	id := strArg(mapFromAny(call(t, a.toolTenantCreate, ctx, args)["tenant"]), "id")
	call(t, a.toolTenantSuspend, ctx, map[string]any{"tenant_id": id})
	again := call(t, a.toolTenantCreate, ctx, args)
	if strArg(mapFromAny(again["tenant"]), "id") != id || tenant(t, db, id).Status != TenantActive || stub.called("containers_start") != 1 {
		t.Fatalf("create on a suspended container tenant should resume it: %s", tenant(t, db, id).Status)
	}

	ids := setupMacPool(t, a, ctx, stub, 1)
	margs := map[string]any{"external_ref": "acct-m", "profile": "mac-m4", "owner_email": "m@example.com", "ssh_keys": testKey}
	mid := strArg(mapFromAny(call(t, a.toolTenantCreate, ctx, margs)["tenant"]), "id")
	call(t, a.toolTenantSuspend, ctx, map[string]any{"tenant_id": mid})
	call(t, a.toolTenantCreate, ctx, margs)
	if tenant(t, db, mid).Status != TenantActive || machine(t, db, ids[0]).State != MachineAssigned {
		t.Fatalf("create on a suspended machine tenant should resume it: %s", tenant(t, db, mid).Status)
	}

	stub.pendingClean = 5
	call(t, a.toolTenantRelease, ctx, map[string]any{"tenant_id": mid, "confirm": mid})
	if tenant(t, db, mid).Status != TenantReleasing {
		t.Fatal("release should be waiting on verify-clean")
	}
	callErr(t, a.toolTenantCreate, ctx, margs, "is being released")
}

func TestCreateUsesTheCallersOwnHold(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupMacPool(t, a, ctx, stub, 1)
	res := call(t, a.toolReservationCreate, ctx, map[string]any{"profile": "mac-m4", "external_ref": "acct-a"})
	rid := strArg(mapFromAny(res["reservation"]), "id")
	b := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-b", "profile": "mac-m4", "owner_email": "b@example.com"})
	if tenant(t, db, strArg(mapFromAny(b["tenant"]), "id")).Status != TenantWaitingForMachine {
		t.Fatal("another account must not take a held machine")
	}
	out := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-a", "profile": "mac-m4", "owner_email": "a@example.com"})
	ta := tenant(t, db, strArg(mapFromAny(out["tenant"]), "id"))
	if ta.MachineID != ids[0] || ta.ReservationID != rid || ta.Status != TenantActive {
		t.Fatalf("holder should get its machine without passing reservation_id: %+v", ta)
	}
	if r, _ := dbReservationGet(db, rid); r.Status != "confirmed" {
		t.Fatalf("hold = %s", r.Status)
	}
}

func TestOfflineHeldMachineIsNotAssigned(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupMacPool(t, a, ctx, stub, 1)
	res := call(t, a.toolReservationCreate, ctx, map[string]any{"profile": "mac-m4", "external_ref": "acct-off"})
	rid := strArg(mapFromAny(res["reservation"]), "id")
	off := false
	stub.instances[100].Connected = &off // switched off, Instances still says ready
	_ = a.tick(ctx)
	if m := machine(t, db, ids[0]); m.Online {
		t.Fatal("a disconnected machine must be offline")
	}
	out := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-off", "profile": "mac-m4", "owner_email": "o@example.com", "reservation_id": rid})
	tn := tenant(t, db, strArg(mapFromAny(out["tenant"]), "id"))
	if tn.MachineID != 0 || tn.Status != TenantWaitingForMachine {
		t.Fatalf("offline held machine was assigned: %+v", tn)
	}
	if r, _ := dbReservationGet(db, rid); r.Status != "released" {
		t.Fatalf("unusable hold should be released, got %s", r.Status)
	}
	// It comes back: the waiting tenant gets it.
	on := true
	stub.instances[100].Connected = &on
	_ = a.tick(ctx)
	if got := tenant(t, db, tn.ID); got.MachineID != ids[0] || got.Status != TenantActive {
		t.Fatalf("tenant after machine came back = %s machine=%d", got.Status, got.MachineID)
	}
}

func TestDestructiveTenantToolsNeedConfirm(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	setupMacPool(t, a, ctx, stub, 1)
	id := strArg(mapFromAny(call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-d", "profile": "mac-m4", "owner_email": "d@example.com"})["tenant"]), "id")
	callErr(t, a.toolTenantRelease, ctx, map[string]any{"tenant_id": id}, "confirm must be the tenant id")
	callErr(t, a.toolTenantRelease, ctx, map[string]any{"external_ref": "acct-d", "confirm": "acct-d"}, "confirm must be the tenant id")
	callErr(t, a.toolTenantReinstall, ctx, map[string]any{"tenant_id": id}, "confirm must be the tenant id")
	if tenant(t, db, id).Status != TenantActive {
		t.Fatal("refused calls must not change the tenant")
	}
	// Already released: repeating without confirm stays a no-op.
	call(t, a.toolTenantRelease, ctx, map[string]any{"tenant_id": id, "confirm": id})
	rel := call(t, a.toolTenantRelease, ctx, map[string]any{"external_ref": "acct-d"})
	if rel["already_released"] != true {
		t.Fatalf("repeat release = %v", rel)
	}
}

func TestSlowStepRunsOnce(t *testing.T) {
	prevRenew := leaseRenewEvery
	leaseRenewEvery = 5 * time.Millisecond
	t.Cleanup(func() { leaseRenewEvery = prevRenew })
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	call(t, a.toolProfileUpsert, ctx, map[string]any{"key": "vps", "backend": "machine_on_demand", "product_key": "ssh-access", "settings": map[string]any{"provider": "hetzner", "server_type": "cx22"}})
	fired := false
	stub.onCall = map[string]func(){"instance_create": func() {
		if fired {
			return
		}
		fired = true
		// The provider takes longer than the lock; the worker ticks meanwhile.
		advanceClock(t, leaseDuration+time.Minute)
		time.Sleep(50 * time.Millisecond)
		_ = a.tick(ctx)
	}}
	out := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-s", "profile": "vps", "owner_email": "s@example.com"})
	if n := stub.called("instance_create"); n != 1 {
		t.Fatalf("instance_create ran %d times", n)
	}
	if tenant(t, db, strArg(mapFromAny(out["tenant"]), "id")).InstanceID == 0 {
		t.Fatal("instance not recorded")
	}
}

func TestWorkloadStartIsAdoptedAfterTimeout(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	call(t, a.toolProfileUpsert, ctx, map[string]any{"key": "vps-app", "backend": "machine_on_demand", "product_key": "apteva", "settings": map[string]any{"provider": "hetzner", "server_type": "cx22"}})
	stub.runErrStarted = true // containers_run times out after starting the container
	out := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-w2", "profile": "vps-app", "owner_email": "w@example.com"})
	id := strArg(mapFromAny(out["tenant"]), "id")
	stub.instances[tenant(t, db, id).InstanceID].Status = "ready"
	advanceClock(t, time.Minute)
	_ = a.tick(ctx)
	tn := tenant(t, db, id)
	if tn.Status != TenantActive || tn.WorkloadID == "" {
		t.Fatalf("tenant = %s workload=%q (%s)", tn.Status, tn.WorkloadID, tn.LastError)
	}
	if n := stub.called("containers_run"); n != 1 {
		t.Fatalf("containers_run ran %d times", n)
	}
}

func TestExpectedOSIsChecked(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	setupMacPool(t, a, ctx, stub, 0)
	stub.addInstance(310, "macos", "ready")
	mid := int64Arg(mapFromAny(call(t, a.toolMachineAdd, ctx, map[string]any{"pool_key": "macs", "instance_id": 310, "expected_os": "15", "serial": "S", "management_user": "admin"})["machine"]), "id")
	call(t, a.toolMachineUpdate, ctx, map[string]any{"machine_id": mid, "validated": true, "confirm": fmt.Sprint(mid)})
	stub.osVersion = "14.6"
	call(t, a.toolMachineInspect, ctx, map[string]any{"machine_id": mid})
	if m := machine(t, db, mid); m.State != MachineQuarantined || !strings.Contains(m.StateReason, "expected \"15\"") {
		t.Fatalf("machine = %s (%s)", m.State, m.StateReason)
	}
	stub.osVersion = "15.3"
	call(t, a.toolMachineInspect, ctx, map[string]any{"machine_id": mid})
	if m := machine(t, db, mid); m.State != MachineAvailable {
		t.Fatalf("machine on the expected OS = %s (%s)", m.State, m.StateReason)
	}
	if osMatches("15.30", "15.3") || !osMatches("15.3.1", "15.3") || !osMatches("ubuntu 24.04", "Ubuntu 24.04") {
		t.Fatal("osMatches prefix rules")
	}
}

func TestResolveStuckTenant(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupMacPool(t, a, ctx, stub, 1)
	id := strArg(mapFromAny(call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-q", "profile": "mac-m4", "owner_email": "q@example.com", "ssh_keys": testKey})["tenant"]), "id")
	stub.failStep = "revoke"
	call(t, a.toolTenantSuspend, ctx, map[string]any{"tenant_id": id})
	if tenant(t, db, id).Status != TenantSuspending || machine(t, db, ids[0]).State != MachineQuarantined {
		t.Fatalf("setup: tenant=%s machine=%s", tenant(t, db, id).Status, machine(t, db, ids[0]).State)
	}
	callErr(t, a.toolTenantResolve, ctx, map[string]any{"tenant_id": id, "action": "release", "confirm": id}, "reviewed=true")
	callErr(t, a.toolTenantResolve, ctx, map[string]any{"tenant_id": id, "action": "release", "reviewed": true}, "confirm must be")
	callErr(t, a.toolTenantResolve, ctx, map[string]any{"tenant_id": id, "action": "nope", "reviewed": true, "confirm": id}, "action must be")
	// revoke keeps failing on this host: erase it without revoking first.
	before := len(stub.commands)
	call(t, a.toolTenantResolve, ctx, map[string]any{"tenant_id": id, "action": "force_clean", "reviewed": true, "confirm": id, "actor": "ops"})
	if tenant(t, db, id).Status != TenantReleased || machine(t, db, ids[0]).State != MachineAvailable {
		t.Fatalf("after force_clean: tenant=%s machine=%s (%s)", tenant(t, db, id).Status, machine(t, db, ids[0]).State, machine(t, db, ids[0]).StateReason)
	}
	for _, c := range stub.commands[before:] {
		if strings.Contains(c, "'revoke'") {
			t.Fatal("force_clean must not run revoke")
		}
	}
	stub.failStep = ""

	// resume path
	id2 := strArg(mapFromAny(call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-q2", "profile": "mac-m4", "owner_email": "q@example.com", "ssh_keys": testKey})["tenant"]), "id")
	call(t, a.toolMachineQuarantine, ctx, map[string]any{"machine_id": ids[0], "reason": "noise"})
	call(t, a.toolTenantResolve, ctx, map[string]any{"tenant_id": id2, "action": "resume", "reviewed": true, "confirm": id2})
	if tenant(t, db, id2).Status != TenantActive || machine(t, db, ids[0]).State != MachineAssigned {
		t.Fatalf("after resume: tenant=%s machine=%s", tenant(t, db, id2).Status, machine(t, db, ids[0]).State)
	}
	callErr(t, a.toolTenantResolve, ctx, map[string]any{"tenant_id": id2, "action": "resume", "reviewed": true, "confirm": id2}, "nothing to resolve")
}

func TestTenantUpdateLinksMigratedTenant(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	id := strArg(mapFromAny(call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "tmp", "profile": "starter", "owner_email": "u@example.com"})["tenant"]), "id")
	if _, err := db.Exec(`UPDATE hosting_tenants SET external_ref='' WHERE id=?`, id); err != nil { // as migrated from 1.x
		t.Fatal(err)
	}
	call(t, a.toolTenantUpdate, ctx, map[string]any{"tenant_id": id, "external_ref": "acct-u", "owner_email": "New@Example.com", "metadata": map[string]any{"saas_account": 7}})
	got := tenant(t, db, id)
	if got.ExternalRef != "acct-u" || got.OwnerEmail != "new@example.com" || mapFromAny(got.Metadata)["saas_account"] == nil {
		t.Fatalf("tenant = %+v", got)
	}
	callErr(t, a.toolTenantUpdate, ctx, map[string]any{"tenant_id": id, "external_ref": "acct-v"}, "confirm must be")
	call(t, a.toolTenantUpdate, ctx, map[string]any{"tenant_id": id, "external_ref": "acct-v", "confirm": id})
	other := strArg(mapFromAny(call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-x", "profile": "starter", "owner_email": "x@example.com"})["tenant"]), "id")
	callErr(t, a.toolTenantUpdate, ctx, map[string]any{"tenant_id": other, "external_ref": "acct-v", "confirm": other}, "already has a live tenant")
	// SaaS can now drive the migrated tenant by its account id.
	call(t, a.toolTenantSuspend, ctx, map[string]any{"external_ref": "acct-v"})
	if tenant(t, db, id).Status != TenantSuspended {
		t.Fatal("suspend by the new external_ref")
	}
}

func TestMigrationHandlesMissingRowsAndReleasedHostnames(t *testing.T) {
	db := openTestDB(t)
	applyMigrations(t, db, "003_addons_metering.sql")
	mustExec := func(q string, args ...any) {
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`PRAGMA foreign_keys=OFF`)
	mustExec(`INSERT INTO hosting_products (key, name) VALUES ('apteva', 'Apteva')`)
	mustExec(`INSERT INTO hosting_plans (key, name, image, cpu, memory_mb, storage_mb) VALUES ('wp', 'WP', 'img', 1, 1024, 2048)`)
	mustExec(`INSERT INTO hosting_plan_bindings (plan_key, product_key) VALUES ('wp', 'wordpress-custom')`) // product row gone
	mustExec(`INSERT INTO hosting_customers (id, email) VALUES (1, 'a@example.com')`)
	mustExec(`INSERT INTO hosting_tenants (id, customer_id, workload_id, slug, default_hostname, owner_email, plan_key, status) VALUES ('htn_wp', 1, 'wl1', 'blog', 'blog.hosted.example.com', 'a@example.com', 'wp', 'active')`)
	mustExec(`INSERT INTO hosting_tenants (id, customer_id, workload_id, slug, default_hostname, owner_email, plan_key, status) VALUES ('htn_gone', 1, 'wl2', 'shop', 'shop.hosted.example.com', 'a@example.com', 'wp', 'deleted')`)
	mustExec(`INSERT INTO hosting_tenants (id, customer_id, workload_id, slug, default_hostname, owner_email, plan_key, status) VALUES ('htn_orphan', 1, 'wl3', 'lost', 'lost.hosted.example.com', 'a@example.com', 'vanished-plan', 'active')`)
	mustExec(`PRAGMA foreign_keys=ON`)
	applyMigrations(t, db, "")

	if p, _ := dbProfileGet(db, "wp"); p == nil || p.ProductKey != "wordpress-custom" {
		t.Fatalf("plan with a missing product must still become a profile: %+v", p)
	}
	if p, _ := dbProfileGet(db, "vanished-plan"); p == nil || p.Status != "archived" {
		t.Fatalf("tenant with a missing plan needs an archived placeholder profile: %+v", p)
	}
	if got, _ := dbTenantGet(db, "htn_orphan"); got == nil || got.Status != TenantActive {
		t.Fatal("orphan tenant lost")
	}
	gone, _ := dbTenantGet(db, "htn_gone")
	if gone.Status != TenantReleased || gone.DefaultHostname != "" {
		t.Fatalf("released tenant keeps hostname %q", gone.DefaultHostname)
	}
	rows, _ := db.Query(`PRAGMA foreign_key_check`)
	if rows.Next() {
		t.Fatal("foreign key problems after migration")
	}
	rows.Close()

	// The released slug can be used again on the migrated database.
	a := &App{}
	manifest := a.Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{"default_base_domain": "hosted.example.com", "default_image": "apteva:test"}, newStub(), nil)
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-shop", "profile": "starter", "owner_email": "s@example.com", "slug": "shop"})
}
