package main

import (
	"strings"
	"testing"
)

func TestTenantSpawnEnvDisablesChildIngress(t *testing.T) {
	t.Setenv("APTEVA_INGRESS_ENABLED", "1")
	t.Setenv("APTEVA_HTTP_LISTEN_ADDR", ":80")
	t.Setenv("APTEVA_HTTPS_LISTEN_ADDR", ":443")

	env := tenantSpawnEnv("/tmp/apteva-tenant", 43559, "tnt_test")
	got := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			got[k] = v
		}
	}
	if got["APTEVA_HOME"] != "/tmp/apteva-tenant" {
		t.Fatalf("APTEVA_HOME = %q", got["APTEVA_HOME"])
	}
	if got["PORT"] != "43559" || got["QUIET"] != "1" {
		t.Fatalf("base env mismatch: PORT=%q QUIET=%q", got["PORT"], got["QUIET"])
	}
	if got["APTEVA_INGRESS_ENABLED"] != "0" {
		t.Fatalf("APTEVA_INGRESS_ENABLED = %q", got["APTEVA_INGRESS_ENABLED"])
	}
	if got["APTEVA_HTTP_LISTEN_ADDR"] != "" || got["APTEVA_HTTPS_LISTEN_ADDR"] != "" {
		t.Fatalf("child edge listeners not cleared: http=%q https=%q", got["APTEVA_HTTP_LISTEN_ADDR"], got["APTEVA_HTTPS_LISTEN_ADDR"])
	}
	if got["DEPLOY_RELEASE_PORT_RANGE_START"] != "44559" || got["DEPLOY_RELEASE_PORT_RANGE_END"] != "45458" {
		t.Fatalf("deploy port range = %s-%s", got["DEPLOY_RELEASE_PORT_RANGE_START"], got["DEPLOY_RELEASE_PORT_RANGE_END"])
	}
	if got["CODE_DEV_PORT_RANGE_START"] != "45459" || got["CODE_DEV_PORT_RANGE_END"] != "45558" {
		t.Fatalf("code dev port range = %s-%s", got["CODE_DEV_PORT_RANGE_START"], got["CODE_DEV_PORT_RANGE_END"])
	}
	if got["APTEVA_DELEGATED_DNS_TENANT_ID"] != "tnt_test" {
		t.Fatalf("APTEVA_DELEGATED_DNS_TENANT_ID = %q", got["APTEVA_DELEGATED_DNS_TENANT_ID"])
	}
}

func TestTenantSpawnEnvStripsParentAppIdentity(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "parent-app-token")
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "parent-outbound-token")
	t.Setenv("APTEVA_INSTALL_ID", "42")
	t.Setenv("APTEVA_PROJECT_ID", "parent-project")
	t.Setenv("APTEVA_APP_CONFIG", `{"parent":true}`)
	t.Setenv("APTEVA_APP_PORT", "5555")
	t.Setenv("APTEVA_GATEWAY_URL", "http://parent.invalid")
	t.Setenv("APTEVA_PUBLIC_URL", "https://parent.invalid")
	t.Setenv("DB_PATH", "/tmp/parent-app.db")

	env := tenantSpawnEnv("/tmp/apteva-tenant", 43559, "tnt_test")
	got := map[string]string{}
	for _, kv := range env {
		if key, value, ok := strings.Cut(kv, "="); ok {
			got[key] = value
		}
	}
	for _, key := range []string{
		"APTEVA_APP_TOKEN", "APTEVA_OUTBOUND_TOKEN", "APTEVA_INSTALL_ID",
		"APTEVA_PROJECT_ID", "APTEVA_APP_CONFIG", "APTEVA_APP_PORT",
		"APTEVA_GATEWAY_URL", "APTEVA_PUBLIC_URL", "DB_PATH",
	} {
		if value, exists := got[key]; exists {
			t.Fatalf("%s leaked into tenant environment as %q", key, value)
		}
	}
	if got["APTEVA_DELEGATED_DNS_FLEET_URL"] != "http://127.0.0.1:5555" {
		t.Fatalf("delegated Fleet URL = %q", got["APTEVA_DELEGATED_DNS_FLEET_URL"])
	}
	if got["APTEVA_DELEGATED_DNS_TOKEN"] != "parent-app-token" {
		t.Fatalf("delegated token was not copied explicitly")
	}
	if got["APTEVA_DELEGATED_DNS_PROJECT_ID"] != "parent-project" {
		t.Fatalf("delegated project was not copied explicitly")
	}
}
