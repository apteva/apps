package main

import (
	"encoding/json"
	"os"
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
	return &sdk.PlatformInstance{ID: id, Name: "Trading Agent", Status: "running", ProjectID: "project-a"}, nil
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
	if storage["app_name"] != "storage" {
		t.Fatalf("storage status trusted legacy agent-name lookup: %#v", storage)
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

func TestSimulationAndFirmwareArtifactsPersistThroughStorageBinding(t *testing.T) {
	platform := &pcbBindingPlatform{bindings: map[string]any{"storage": float64(41)}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	store := testStore(t)
	definition, _ := json.Marshal(sensorNodeExample())
	canonical, _, hash, err := normalizeDefinition(definition, "Sensor node")
	if err != nil {
		t.Fatal(err)
	}
	design, err := store.CreateDesign("project-a", "Sensor node", canonical, nil, hash, "", "")
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{store: store, ctx: ctx, project: "project-a", artifactRoot: t.TempDir()}
	simulation, err := service.Simulate(design.ID, 0, SimulationOptions{DurationUS: 200, StepUS: 100})
	if err != nil {
		t.Fatal(err)
	}
	firmware, err := service.Firmware(design.ID, 0, FirmwareOptions{Source: `void setup(){Serial.begin(115200);} void loop(){Serial.println(sensor.readTemperature()); delay(1);}`, Iterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	if simulation.Artifact.StorageFileID != "91" || firmware.Artifact.StorageFileID != "91" {
		t.Fatalf("artifacts did not retain Storage IDs: simulation=%q firmware=%q", simulation.Artifact.StorageFileID, firmware.Artifact.StorageFileID)
	}
	if simulation.Artifact.Name == firmware.Artifact.Name || simulation.Artifact.LocalPath == firmware.Artifact.LocalPath {
		t.Fatal("simulation and firmware artifacts collided")
	}
	for _, path := range []string{simulation.Artifact.LocalPath, firmware.Artifact.LocalPath} {
		if info, statErr := os.Stat(path); statErr != nil || info.Size() == 0 {
			t.Fatalf("artifact %q was not retained locally: %v", path, statErr)
		}
	}
	if platform.app != "storage" || platform.tool != "files_upload" {
		t.Fatalf("artifact persistence bypassed canonical Storage binding: %s/%s", platform.app, platform.tool)
	}
}
