package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

const scalewayStorageTypesFixture = `{"servers":{
  "DEV1-L":{"ncpus":4,"ram":8589934592,"arch":"x86_64","capabilities":{"block_storage":true},"volumes_constraint":{"min_size":0,"max_size":80000000000},"per_volume_constraint":{"l_ssd":{"min_size":1000000000,"max_size":800000000000}}},
  "POP2-HC-2C-4G":{"ncpus":2,"ram":4294967296,"arch":"x86_64","capabilities":{"block_storage":true},"volumes_constraint":{"min_size":0,"max_size":0},"per_volume_constraint":{"l_ssd":{"min_size":0,"max_size":0}}}
}}`

const scalewayStorageImagesFixture = `{"images":[
  {"id":"image-local","name":"Ubuntu 24.04 local","arch":"x86_64","public":true,"state":"available","root_volume":{"size":10000000000,"volume_type":"l_ssd"}},
  {"id":"image-sbs","name":"Ubuntu 24.04 SBS","arch":"x86_64","public":true,"state":"available","root_volume":{"size":10000000000,"volume_type":"sbs_snapshot"}}
]}`

type scalewayStoragePlatform struct {
	tk.BasePlatformClient
	serverData json.RawMessage
	volumeData json.RawMessage
	tools      []string
}

func (p *scalewayStoragePlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"provider": float64(7)}}, nil
}

func (p *scalewayStoragePlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "scaleway"}, nil
}

func (p *scalewayStoragePlatform) GetConnectionPublicConfig(id int64) (*sdk.ConnectionPublicConfig, error) {
	return &sdk.ConnectionPublicConfig{ConnectionID: id, Slug: "scaleway", Fields: map[string]string{"project_id": "project-1"}}, nil
}

func (p *scalewayStoragePlatform) ExecuteIntegrationTool(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
	p.tools = append(p.tools, tool)
	data := json.RawMessage(`{}`)
	switch tool {
	case "server_types_list":
		data = json.RawMessage(scalewayStorageTypesFixture)
	case "image_list":
		data = json.RawMessage(scalewayStorageImagesFixture)
	case "dedibox_offers_list":
		data = json.RawMessage(`{"offers":[]}`)
	case "apple_products_list":
		data = json.RawMessage(`{"products":[]}`)
	case "server_get":
		data = p.serverData
	case "instance_volume_get", "volume_get":
		data = p.volumeData
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: data}, nil
}

func TestParseScalewayStorageConstraints(t *testing.T) {
	types, err := parseProviderServerTypes("scaleway", json.RawMessage(scalewayStorageTypesFixture))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ServerType{}
	for _, serverType := range types {
		byName[serverType.Name] = serverType
	}
	dev := byName["DEV1-L"]
	if dev.DiskGB != 80 || len(dev.BootStorage) != 2 || dev.BootStorage[0].StorageClass != "local" || dev.BootStorage[0].MaxSizeGB != 80 {
		t.Fatalf("DEV1-L storage = disk %d, options %#v", dev.DiskGB, dev.BootStorage)
	}
	pop := byName["POP2-HC-2C-4G"]
	if pop.DiskGB != 0 || len(pop.BootStorage) != 1 || pop.BootStorage[0].StorageClass != "block" {
		t.Fatalf("POP2-HC storage = disk %d, options %#v", pop.DiskGB, pop.BootStorage)
	}
}

func TestScalewayBootStorageRequestsMapLocalAndBlock(t *testing.T) {
	platform := &scalewayStoragePlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	tests := []struct {
		name         string
		size         string
		image        string
		sizeGB       int
		storageClass string
		wantType     string
		wantOmitted  bool
	}{
		{name: "DEV1-L full local SSD", size: "DEV1-L", image: "image-local", sizeGB: 80, storageClass: "local", wantType: "l_ssd", wantOmitted: true},
		{name: "DEV1-L custom local SSD", size: "DEV1-L", image: "image-local", sizeGB: 40, storageClass: "local", wantType: "l_ssd"},
		{name: "POP2-HC block storage", size: "POP2-HC-2C-4G", image: "image-sbs", sizeGB: 80, storageClass: "block", wantType: "sbs_volume"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := CreateInstanceInput{Name: "vm", Provider: "scaleway", ProviderConnectionID: 7, Region: "fr-par-1", Size: tt.size, Image: tt.image, Storage: InstanceStorageRequest{Boot: &BootStorageRequest{SizeGB: tt.sizeGB, StorageClass: tt.storageClass, DeletePolicy: "with_instance"}}}
			if err := validateStorageRequest("scaleway", in.Storage); err != nil {
				t.Fatal(err)
			}
			if err := validateScalewayBootStorage(ctx, &in); err != nil {
				t.Fatal(err)
			}
			_, args, err := apiProviderCreateRequest(ctx, "scaleway", in, "ssh-ed25519 AAAA test")
			if err != nil {
				t.Fatal(err)
			}
			volumesValue, present := args["volumes"]
			if tt.wantOmitted {
				if present {
					t.Fatalf("full-capacity local root must omit volumes, got %#v", volumesValue)
				}
				return
			}
			if !present {
				t.Fatal("explicit root volume mapping is missing")
			}
			volumes := volumesValue.(map[string]any)
			boot := volumes["0"].(map[string]any)
			if boot["volume_type"] != tt.wantType || boot["size"] != int64(tt.sizeGB)*1_000_000_000 {
				t.Fatalf("boot mapping = %#v", boot)
			}
			if _, present := boot["name"]; present {
				t.Fatalf("image-derived root must not include name: %#v", boot)
			}
			if _, present := boot["boot"]; present {
				t.Fatalf("image-derived root must not include boot: %#v", boot)
			}
		})
	}
}

func TestScalewayRejectsLocalStorageOnBlockOnlyType(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(&scalewayStoragePlatform{}))
	in := CreateInstanceInput{Provider: "scaleway", ProviderConnectionID: 7, Region: "fr-par-1", Size: "POP2-HC-2C-4G", Image: "image-local", Storage: InstanceStorageRequest{Boot: &BootStorageRequest{SizeGB: 80, StorageClass: "local"}}}
	if err := validateScalewayBootStorage(ctx, &in); err == nil || !strings.Contains(err.Error(), "does not support local") {
		t.Fatalf("error = %v", err)
	}
}

func TestScalewayRejectsIncompatibleBootImage(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(&scalewayStoragePlatform{}))
	in := CreateInstanceInput{Provider: "scaleway", ProviderConnectionID: 7, Region: "fr-par-1", Size: "DEV1-L", Image: "image-sbs", Storage: InstanceStorageRequest{Boot: &BootStorageRequest{SizeGB: 80, StorageClass: "local"}}}
	if err := validateScalewayBootStorage(ctx, &in); err == nil || !strings.Contains(err.Error(), "cannot create a l_ssd") {
		t.Fatalf("error = %v", err)
	}
}

func TestScalewayPersistsVerifiedBootVolume(t *testing.T) {
	platform := &scalewayStoragePlatform{
		serverData: json.RawMessage(`{"server":{"volumes":{"0":{"id":"local-1","volume_type":"l_ssd","boot":true}}}}`),
		volumeData: json.RawMessage(`{"volume":{"id":"local-1","size":80000000000,"volume_type":"l_ssd"}}`),
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{Name: "dev", Provider: "scaleway", ProviderConnectionID: 7, ProviderID: "server-1", Region: "fr-par-1", Status: "provisioning", Storage: InstanceStorageRequest{Boot: &BootStorageRequest{SizeGB: 80, StorageClass: "local", Tier: "local", ProviderType: "l_ssd", DeletePolicy: "with_instance"}}})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := verifyAndPersistScalewayBootStorage(ctx, inst)
	if err != nil {
		t.Fatal(err)
	}
	if actual.ProviderType != "l_ssd" || actual.SizeGB != 80 {
		t.Fatalf("actual = %#v", actual)
	}
	volumes, err := dbListVolumes(ctx.AppDB(), inst.ID, "")
	if err != nil || len(volumes) != 1 || volumes[0].Role != "boot" || volumes[0].StorageClass != "local" || volumes[0].SizeGB != 80 {
		t.Fatalf("volumes = %#v, err=%v", volumes, err)
	}
	if !containsString(platform.tools, "instance_volume_get") {
		t.Fatalf("tools = %#v", platform.tools)
	}
}

func TestScalewayRootFilesystemMustUseRequestedSpace(t *testing.T) {
	previous := runScalewayRootStorageCommand
	t.Cleanup(func() { runScalewayRootStorageCommand = previous })
	runScalewayRootStorageCommand = func(*Instance, string, time.Duration) (string, int, error) {
		return "8000000000\n", 0, nil
	}
	if err := verifyScalewayRootFilesystem(&Instance{}, 80); err == nil || !strings.Contains(err.Error(), "only 8.0 GB") {
		t.Fatalf("error = %v", err)
	}
}
