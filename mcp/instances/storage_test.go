package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type recordingVolumePlatform struct {
	tk.BasePlatformClient
	slug        string
	connections []int64
	tools       []string
	args        []map[string]any
}

func (p *recordingVolumePlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"provider": float64(7)}}, nil
}

func (p *recordingVolumePlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: p.slug}, nil
}

func (p *recordingVolumePlatform) ExecuteIntegrationTool(connectionID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.connections = append(p.connections, connectionID)
	p.tools = append(p.tools, tool)
	p.args = append(p.args, input)
	data := json.RawMessage(`{}`)
	if tool == "volume_create" {
		data = json.RawMessage(`{"volume":{"id":"volume-1","size_gigabytes":80}}`)
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: data}, nil
}

func TestStorageCapabilities_DistinguishesBootAndData(t *testing.T) {
	scaleway := storageCapabilities("scaleway")
	if !scaleway.BootSizeConfigurable || !scaleway.DataVolumes || !scaleway.DynamicAttach || !scaleway.GuestPrepare || len(scaleway.GuestFilesystems) != 2 {
		t.Fatalf("scaleway capabilities = %#v", scaleway)
	}
	if !containsString(scaleway.StorageClasses, "local") || !containsString(scaleway.StorageClasses, "block") {
		t.Fatalf("scaleway storage classes = %#v", scaleway.StorageClasses)
	}
	runpod := storageCapabilities("runpod")
	if !runpod.BootSizeConfigurable || !runpod.DataVolumes || runpod.DynamicAttach || !strings.Contains(runpod.Notes, "created") {
		t.Fatalf("runpod capabilities = %#v", runpod)
	}
	contabo := storageCapabilities("contabo")
	if contabo.DataVolumes || !strings.Contains(contabo.Notes, "Object Storage") {
		t.Fatalf("contabo capabilities = %#v", contabo)
	}
}

func TestVolumeCreate_AttachesAndRecordsDigitalOceanVolume(t *testing.T) {
	platform := &recordingVolumePlatform{slug: "digitalocean"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{
		Name: "vm-1", Provider: "digitalocean", ProviderConnectionID: 7, ProviderID: "123", Region: "ams3", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}

	volume, err := createManagedVolume(ctx, CreateVolumeInput{InstanceID: inst.ID, Name: "data-1", SizeGB: 80, Tier: "provider-default"})
	if err != nil {
		t.Fatal(err)
	}
	if volume.ProviderVolumeID != "volume-1" || volume.InstanceID == nil || *volume.InstanceID != inst.ID || volume.Status != "attached" || volume.DeletePolicy != "retain" {
		t.Fatalf("volume = %#v", volume)
	}
	if len(platform.tools) != 2 || platform.tools[0] != "volume_create" || platform.tools[1] != "volume_action" {
		t.Fatalf("tools = %#v", platform.tools)
	}
	if platform.args[1]["type"] != "attach" || platform.args[1]["droplet_id"] != int64(123) {
		t.Fatalf("attach args = %#v", platform.args[1])
	}
	for _, connectionID := range platform.connections {
		if connectionID != 7 {
			t.Fatalf("connection id = %d", connectionID)
		}
	}
}

func TestVolumeDelete_RequiresDetachAndConfirmation(t *testing.T) {
	db := openTestDB(t)
	instanceID := int64(4)
	volume, err := dbCreateVolume(db, CreateVolumeInput{InstanceID: instanceID, Provider: "digitalocean", ProviderConnectionID: 7, Name: "data", SizeGB: 10, Tier: "provider-default", DeletePolicy: "retain"}, "vol-1", "block", "attached", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if volume.InstanceID == nil {
		t.Fatal("volume should be attached")
	}
	app := &App{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(&recordingVolumePlatform{slug: "digitalocean"}))
	if _, err := app.toolVolumeDelete(ctx, map[string]any{"id": volume.ID}); err == nil || !strings.Contains(err.Error(), "confirm=true") {
		t.Fatalf("delete without confirmation err = %v", err)
	}
}

func TestBootStorageRequestMapsToProviderCreateArguments(t *testing.T) {
	in := CreateInstanceInput{Name: "vm", Region: "fr-par-1", Size: "POP2-HC-2C-4G", Image: "image-1", Storage: InstanceStorageRequest{Boot: &BootStorageRequest{SizeGB: 80, Tier: "balanced", DeletePolicy: "with_instance"}}}
	// Scaleway project discovery is integration-backed, so assert the pure
	// providers here and cover Scaleway through the integration contract test.
	_, aws, err := apiProviderCreateRequest(nil, "aws-ec2", in, "ssh-ed25519 AAAA test")
	if err != nil {
		t.Fatal(err)
	}
	if aws["BlockDeviceMapping.1.Ebs.VolumeSize"] != 80 || aws["BlockDeviceMapping.1.Ebs.VolumeType"] != "gp3" || aws["BlockDeviceMapping.1.Ebs.DeleteOnTermination"] != true {
		t.Fatalf("aws boot mapping = %#v", aws)
	}
	in.Region = "eu-west-101"
	_, huawei, err := apiProviderCreateRequest(nil, "huawei-cloud", in, "ssh-ed25519 AAAA test")
	if err == nil || huawei != nil {
		// nil context cannot discover a Huawei network; the boot mapping is
		// exercised in provider request tests with a platform stub.
		if err == nil {
			t.Fatalf("expected network discovery error")
		}
	}
}
