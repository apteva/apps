package main

import (
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestManifestParses(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.Name != "workflows" {
		t.Errorf("Name = %q, want workflows", m.Name)
	}
	if m.Version == "" {
		t.Error("Version empty")
	}
	if len(m.Provides.MCPTools) == 0 {
		t.Error("expected MCP tools in manifest")
	}
	if m.Runtime.HealthCheck != "/subscriber/health" {
		t.Errorf("health_check = %q, want /subscriber/health", m.Runtime.HealthCheck)
	}
	for _, key := range []string{"APTEVA_GATEWAY_URL", "APTEVA_APP_TOKEN"} {
		if env, ok := m.Runtime.Env[key]; !ok || env.From != "platform" {
			t.Errorf("runtime.env[%s] = %+v, want from: platform", key, env)
		}
	}
}

func TestAppManifestRoundtrips(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "workflows" {
		t.Errorf("Name = %q, want workflows", m.Name)
	}
}

func TestEmbeddedManifestMatchesInstallManifest(t *testing.T) {
	diskBytes, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := sdk.ParseManifest(diskBytes)
	if err != nil {
		t.Fatal(err)
	}
	embedded := (&App{}).Manifest()
	if embedded.Name != disk.Name || embedded.Version != disk.Version {
		t.Fatalf("identity drift: embedded=%s@%s disk=%s@%s",
			embedded.Name, embedded.Version, disk.Name, disk.Version)
	}
	if embedded.Runtime.HealthCheck != disk.Runtime.HealthCheck {
		t.Fatalf("health_check drift: embedded=%q disk=%q",
			embedded.Runtime.HealthCheck, disk.Runtime.HealthCheck)
	}
	if embedded.MinAptevaVersion != disk.MinAptevaVersion {
		t.Fatalf("min_apteva_version drift: embedded=%q disk=%q",
			embedded.MinAptevaVersion, disk.MinAptevaVersion)
	}
	for _, key := range []string{"APTEVA_GATEWAY_URL", "APTEVA_APP_TOKEN"} {
		if embedded.Runtime.Env[key] != disk.Runtime.Env[key] {
			t.Fatalf("runtime.env[%s] drift: embedded=%+v disk=%+v",
				key, embedded.Runtime.Env[key], disk.Runtime.Env[key])
		}
	}
}

func TestMCPToolsHaveSchemas(t *testing.T) {
	app := &App{}
	tools := app.MCPTools()
	if len(tools) == 0 {
		t.Fatal("no MCP tools declared")
	}
	for _, tool := range tools {
		if tool.Name == "" {
			t.Error("tool with empty name")
		}
		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no InputSchema", tool.Name)
		}
		if tool.Handler == nil && tool.HandlerCtx == nil {
			t.Errorf("tool %q has no handler", tool.Name)
		}
	}
}
