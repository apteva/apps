package main

import (
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestManifestValid(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if err := sdk.ValidateManifest(m); err != nil {
		t.Fatalf("validate manifest: %v", err)
	}
	if m.Name != "containers" {
		t.Fatalf("name=%q", m.Name)
	}
}

func TestManifestFileValid(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	m, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	if err := sdk.ValidateManifest(m); err != nil {
		t.Fatalf("validate apteva.yaml: %v", err)
	}
	if m.Name != "containers" {
		t.Fatalf("name=%q", m.Name)
	}
}

func TestNormalizeRunSpecDefaults(t *testing.T) {
	spec, err := normalizeRunSpec(RunSpec{
		Name:    "demo-nginx",
		Image:   "nginx:alpine",
		Ports:   []PortSpec{{ContainerPort: 80}},
		Volumes: []VolumeSpec{{Name: "data", MountPath: "/data"}},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if spec.RestartPolicy != "unless-stopped" {
		t.Fatalf("restart_policy=%q", spec.RestartPolicy)
	}
	if spec.HealthPath != "/" {
		t.Fatalf("health_path=%q", spec.HealthPath)
	}
	if spec.Ports[0].BindAddr != "127.0.0.1" || spec.Ports[0].Protocol != "tcp" {
		t.Fatalf("port defaults not applied: %+v", spec.Ports[0])
	}
}

func TestNormalizeRunSpecRejectsUnsafeInputs(t *testing.T) {
	bad := []RunSpec{
		{Name: "Bad Name", Image: "nginx"},
		{Name: "ok", Image: ""},
		{Name: "ok", Image: "nginx", HostID: 7},
		{Name: "ok", Image: "nginx", Ports: []PortSpec{{ContainerPort: 70000}}},
		{Name: "ok", Image: "nginx", Volumes: []VolumeSpec{{Name: "data", MountPath: "relative"}}},
	}
	for _, spec := range bad {
		if _, err := normalizeRunSpec(spec); err == nil {
			t.Fatalf("expected error for %+v", spec)
		}
	}
}
