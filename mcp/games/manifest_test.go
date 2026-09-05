package main

// manifest_test — apteva.yaml on disk vs the embedded copy, tool list vs
// App.MCPTools(), and the public-route contract: only /v1/* is NoAuth.

import (
	"os"
	"sort"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestManifest_ParsesAndValidates(t *testing.T) {
	body, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	m, err := sdk.ParseManifest(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Name != "games" {
		t.Errorf("name = %q, want games", m.Name)
	}
	if m.Version == "" {
		t.Error("version empty")
	}
	if m.DB == nil || m.DB.Driver != "sqlite" {
		t.Errorf("db block missing or wrong driver: %+v", m.DB)
	}
	var auth *sdk.RequiredAppRef
	for i := range m.Requires.Apps {
		if m.Requires.Apps[i].Name == "auth" {
			auth = &m.Requires.Apps[i]
		}
	}
	if auth == nil || auth.Optional {
		t.Fatalf("auth must be a required dependency: %+v", auth)
	}
	if !strings.HasPrefix(auth.Version, ">=0.10") {
		t.Errorf("auth version floor = %q, want >=0.10.x (guest identities)", auth.Version)
	}
}

func TestManifest_OnDiskMatchesEmbedded(t *testing.T) {
	disk, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	mDisk, err := sdk.ParseManifest(disk)
	if err != nil {
		t.Fatalf("parse disk manifest: %v", err)
	}
	mEmb, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse embedded manifest: %v", err)
	}
	if mDisk.Version != mEmb.Version || mDisk.Name != mEmb.Name || mDisk.MinAptevaVersion != mEmb.MinAptevaVersion {
		t.Errorf("identity drift: disk=%s/%s/%s embedded=%s/%s/%s",
			mDisk.Name, mDisk.Version, mDisk.MinAptevaVersion, mEmb.Name, mEmb.Version, mEmb.MinAptevaVersion)
	}
	if !equalStrSlices(toolNames(mDisk.Provides.MCPTools), toolNames(mEmb.Provides.MCPTools)) {
		t.Errorf("tool list drift\ndisk: %v\nembd: %v", toolNames(mDisk.Provides.MCPTools), toolNames(mEmb.Provides.MCPTools))
	}
	if !equalStrSlices(routeSpecs(mDisk.Provides.HTTPRoutes), routeSpecs(mEmb.Provides.HTTPRoutes)) {
		t.Errorf("http route drift")
	}
	if !equalStrSlices(configSpecs(mDisk.ConfigSchema), configSpecs(mEmb.ConfigSchema)) {
		t.Errorf("config schema drift")
	}
}

func TestManifest_PublicRoutesOnlyUnderV1(t *testing.T) {
	body, _ := os.ReadFile("apteva.yaml")
	m, _ := sdk.ParseManifest(body)
	sawV1, sawAdmin := false, false
	for _, route := range m.Provides.HTTPRoutes {
		switch route.Prefix {
		case "/v1/":
			sawV1 = true
			if !route.NoAuth {
				t.Error("/v1/ must be no_auth — game builds never hold the platform token")
			}
		case "/admin/":
			sawAdmin = true
			if route.NoAuth {
				t.Error("/admin/ must stay behind the platform token")
			}
		}
		if route.Prefix == "/" && route.NoAuth {
			t.Fatal("root route must never be no_auth")
		}
	}
	if !sawV1 || !sawAdmin {
		t.Fatalf("manifest must declare /v1/ and /admin/ routes")
	}
	for _, r := range (&App{}).HTTPRoutes() {
		if r.NoAuth && !strings.HasPrefix(r.Pattern, "/v1/") {
			t.Errorf("NoAuth route outside /v1: %s", r.Pattern)
		}
		if !r.NoAuth && !strings.HasPrefix(r.Pattern, "/admin/") {
			t.Errorf("token-gated route outside /admin: %s", r.Pattern)
		}
	}
}

func TestMCPTools_MatchManifest(t *testing.T) {
	body, _ := os.ReadFile("apteva.yaml")
	m, _ := sdk.ParseManifest(body)
	declared := toolNames(m.Provides.MCPTools)
	implemented := []string{}
	for _, tool := range (&App{}).MCPTools() {
		implemented = append(implemented, tool.Name)
	}
	sort.Strings(implemented)
	if !equalStrSlices(declared, implemented) {
		t.Errorf("declared vs implemented tool drift\ndeclared: %v\nimpl: %v", declared, implemented)
	}
}

func toolNames(tools []sdk.MCPToolSpec) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	sort.Strings(out)
	return out
}

func routeSpecs(routes []sdk.RouteSpec) []string {
	out := make([]string, len(routes))
	for i, r := range routes {
		auth := "auth"
		if r.NoAuth {
			auth = "noauth"
		}
		out[i] = r.Method + " " + r.Prefix + " " + auth
	}
	sort.Strings(out)
	return out
}

func configSpecs(fields []sdk.ConfigField) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = f.Name + "\x00" + f.Type + "\x00" + f.Default
	}
	sort.Strings(out)
	return out
}

func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
