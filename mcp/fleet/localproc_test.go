package main

import (
	"os"
	"reflect"
	"testing"
)

func TestTenantScopePattern_SanitizesSlug(t *testing.T) {
	got := tenantScopePattern("client.alpha")
	want := "fleet-tenant-client_alpha*.scope"
	if got != want {
		t.Fatalf("tenantScopePattern = %q, want %q", got, want)
	}
}

func TestListTenantScopeUnits_RejectsSlugPrefixCollision(t *testing.T) {
	dir := t.TempDir()
	systemctl := dir + "/systemctl"
	script := `#!/bin/sh
cat <<'EOF'
fleet-tenant-flex-123.scope loaded active running tenant flex
fleet-tenant-flex.scope loaded active running tenant flex legacy
fleet-tenant-flexylead-456.scope loaded active running tenant flexylead
EOF
`
	if err := os.WriteFile(systemctl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := listTenantScopeUnits(systemctl, "flex")
	if err != nil {
		t.Fatalf("listTenantScopeUnits: %v", err)
	}
	want := []string{"fleet-tenant-flex-123.scope", "fleet-tenant-flex.scope"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("units = %v, want %v", got, want)
	}
}

func TestParseProcPPID(t *testing.T) {
	got, ok := parseProcPPID("123 (apteva-server) S 45 123 123 0 -1 4194560")
	if !ok {
		t.Fatal("parseProcPPID returned !ok")
	}
	if got != 45 {
		t.Fatalf("ppid = %d, want 45", got)
	}
}

func TestTenantTreeRoot_UsesOnlyMarkedAncestor(t *testing.T) {
	procs := map[int]procInfo{
		1:   {pid: 1, ppid: 0, cmdline: "/sbin/init"},
		100: {pid: 100, ppid: 1, cmdline: "/root/.apteva/bin/apteva-server"},
		200: {pid: 200, ppid: 100, cmdline: "node /.apteva-fleet/flexylead --port 33071"},
		201: {pid: 201, ppid: 200, cmdline: "/.apteva-fleet/versions/0.24.12/node_modules/apteva/apteva --data-dir /.apteva-fleet/flexylead --port 33071"},
		202: {pid: 202, ppid: 201, cmdline: "/.apteva-fleet/versions/0.24.12/node_modules/apteva/apteva-server"},
	}
	got, ok := tenantTreeRoot(202, "/.apteva-fleet/flexylead", procs)
	if !ok {
		t.Fatal("tenantTreeRoot returned !ok")
	}
	if got != 200 {
		t.Fatalf("root = %d, want 200", got)
	}
}

func TestTenantTreeRoot_RejectsUnmarkedServiceAncestor(t *testing.T) {
	procs := map[int]procInfo{
		1:   {pid: 1, ppid: 0, cmdline: "/sbin/init"},
		100: {pid: 100, ppid: 1, cmdline: "/root/.apteva/bin/apteva-server"},
		202: {pid: 202, ppid: 100, cmdline: "/root/.apteva/bin/apteva-server"},
	}
	if got, ok := tenantTreeRoot(202, "/.apteva-fleet/flexylead", procs); ok {
		t.Fatalf("tenantTreeRoot returned root %d for unmarked service process", got)
	}
}

func TestTenantTreeRootRejectsDataDirSubstring(t *testing.T) {
	marker := "/var/lib/apteva-fleet/acme"
	procs := map[int]procInfo{
		100: {pid: 100, ppid: 1, cmdline: "unrelated --note prefix" + marker + "-other"},
	}
	if got, ok := tenantTreeRoot(100, marker, procs); ok {
		t.Fatalf("substring-only process accepted as tenant root %d", got)
	}
}

func TestProcTreePIDs_LeavesBeforeRoot(t *testing.T) {
	procs := map[int]procInfo{
		200: {pid: 200, ppid: 1},
		201: {pid: 201, ppid: 200},
		202: {pid: 202, ppid: 201},
		203: {pid: 203, ppid: 200},
	}
	got := procTreePIDs(200, procs)
	want := []int{202, 201, 203, 200}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("procTreePIDs = %v, want %v", got, want)
	}
}
