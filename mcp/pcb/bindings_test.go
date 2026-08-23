package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type pcbBindingPlatform struct {
	tk.BasePlatformClient
	bindings map[string]any
	app      string
	tool     string
}

func (p *pcbBindingPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{AppName: "pcb", InstallID: 11, ProjectID: "project-a", Bindings: p.bindings}, nil
}

func (p *pcbBindingPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	return &sdk.PlatformInstance{ID: id, Name: "storage", Status: "running", ProjectID: "project-a"}, nil
}

func (p *pcbBindingPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	slug := "nexar"
	if id == 9 {
		slug = "jlcpcb"
	}
	return &sdk.PlatformConnection{ID: id, AppSlug: slug}, nil
}

func (p *pcbBindingPlatform) CallAppResult(app, tool string, _ map[string]any, out any) error {
	p.app, p.tool = app, tool
	return json.Unmarshal([]byte(`{"id":91}`), out)
}

func TestStorageUploadUsesNativeAppBinding(t *testing.T) {
	platform := &pcbBindingPlatform{bindings: map[string]any{
		"storage": float64(41), "component_data": float64(7), "pcb_fabricator": float64(9),
	}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	service := &Service{ctx: ctx, project: "project-a"}
	id, err := service.uploadStorage("board.svg", "image/svg+xml", []byte("<svg/>"), 3, 4, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if id != "91" || platform.app != "storage" || platform.tool != "files_upload" {
		t.Fatalf("upload = id %q app %q tool %q", id, platform.app, platform.tool)
	}
	status, err := (&App{}).toolProvidersStatus(nil, ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	bindings := status.(map[string]any)
	storage := bindings["storage"].(map[string]any)
	if storage["kind"] != "app" || storage["install_id"] != int64(41) {
		t.Fatalf("storage status does not expose native app binding: %#v", storage)
	}
}

func TestStorageUploadRejectsMissingNativeBinding(t *testing.T) {
	platform := &pcbBindingPlatform{bindings: map[string]any{}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	service := &Service{ctx: ctx, project: "project-a"}
	_, err := service.uploadStorage("board.svg", "image/svg+xml", []byte("<svg/>"), 3, 4, "abc")
	if err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatalf("expected missing-binding error, got %v", err)
	}
}
