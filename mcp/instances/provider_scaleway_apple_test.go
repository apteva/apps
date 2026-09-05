package main

import (
	"encoding/json"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

const appleProductFixture = `{
  "products": [
    {
      "product":"Mac Mini M4 - S","status":"general_availability","locality":{"zone":"fr-par-1"},
      "price":{"retail_price":{"currency_code":"EUR","units":149,"nanos":0}},
      "unit_of_measure":{"unit":"month"},
      "properties":{"apple_silicon":{"server_type":"M4-S"},"hardware":{
        "cpu":{"type":"Apple M4","physical":{"sockets":1,"cores_per_socket":10}},
        "ram":{"size":25769803776},"storage":{"total":256000000000},"gpu":{"count":10,"type":"Apple M4"}
      }}
    },
    {
      "product":"Mac Mini M4 - S","status":"general_availability","locality":{"zone":"fr-par-3"},
      "price":{"retail_price":{"currency_code":"EUR","units":0,"nanos":220000000}},
      "unit_of_measure":{"unit":"hour"},"properties":{"apple_silicon":{"server_type":"M4-S"}}
    },
    {
      "product":"Old Mac","status":"retired","locality":{"zone":"fr-par-1"},
      "properties":{"apple_silicon":{"server_type":"M1-OLD"}}
    }
  ]
}`

func TestParseScalewayAppleProducts(t *testing.T) {
	types, err := parseScalewayAppleProducts(json.RawMessage(appleProductFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 1 {
		t.Fatalf("types=%#v", types)
	}
	got := types[0]
	if got.Name != "apple-silicon/M4-S" || got.Platform != "macos" || got.ResourceClass != "bare_metal" {
		t.Fatalf("identity=%#v", got)
	}
	if got.Cores != 10 || got.MemoryGB != 24 || got.DiskGB != 256 || got.MonthlyPriceEUR != 149 || got.HourlyPriceEUR != 0.22 {
		t.Fatalf("hardware/pricing=%#v", got)
	}
	if len(got.AvailableIn) != 2 || len(got.Accelerators) != 1 || got.Accelerators[0].Count != 1 {
		t.Fatalf("locations/accelerators=%#v", got)
	}
}

func TestParseScalewayAppleImages(t *testing.T) {
	images, err := parseScalewayAppleImages(json.RawMessage(`{"os":[
      {"id":"os-macos","label":"macOS Sequoia","family":"15","version":"15.5","xcode_version":"16.4","supported_server_types":[{"server_type":"M4-S"}]},
      {"id":"os-beta","label":"macOS beta","is_beta":true}
    ]}`), "fr-par-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].Name != "apple-silicon/os-macos" || images[0].Platform != "macos" ||
		len(images[0].CompatibleTypes) != 1 || images[0].CompatibleTypes[0] != "apple-silicon/M4-S" {
		t.Fatalf("images=%#v", images)
	}
}

type scalewayApplePlatform struct {
	tk.BasePlatformClient
	tools []string
	args  []map[string]any
}

func (p *scalewayApplePlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"provider": float64(7)}}, nil
}

func (p *scalewayApplePlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "scaleway"}, nil
}

func (p *scalewayApplePlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.tools = append(p.tools, tool)
	p.args = append(p.args, input)
	data := json.RawMessage(`{}`)
	status := 200
	switch tool {
	case "server_types_list":
		data = json.RawMessage(`{"servers":{}}`)
	case "apple_products_list":
		data = json.RawMessage(appleProductFixture)
	case "api_key_get":
		data = json.RawMessage(`{"access_key":"SCWACCESSKEY","default_project_id":"project-default"}`)
	case "security_group_list":
		data = json.RawMessage(`{"security_groups":[{"id":"sg-1","project":"project-1","project_default":true}]}`)
	case "ssh_key_create":
		data = json.RawMessage(`{"id":"key-created-by-apteva"}`)
	case "apple_server_create":
		data = json.RawMessage(`{"id":"mac-1","type":"M4-S","ip":"203.0.113.20","ssh_username":"m4","deletable_at":"2026-08-09T12:00:00Z"}`)
	case "apple_server_delete", "ssh_key_delete":
		status = 204
		data = json.RawMessage(`null`)
	default:
		return &sdk.ExecuteResult{Success: false, Status: 404, Data: json.RawMessage(`{"error":"unexpected tool"}`)}, nil
	}
	return &sdk.ExecuteResult{Success: true, Status: status, Data: data}, nil
}

type scalewayConfiguredPlatform struct {
	*scalewayApplePlatform
}

func (p *scalewayConfiguredPlatform) GetConnectionPublicConfig(id int64) (*sdk.ConnectionPublicConfig, error) {
	return &sdk.ConnectionPublicConfig{ConnectionID: id, Slug: "scaleway", Fields: map[string]string{"access_key": "SCWACCESSKEY"}}, nil
}

func TestScalewayProjectDefaultsFromAPIKey(t *testing.T) {
	base := &scalewayApplePlatform{}
	platform := &scalewayConfiguredPlatform{scalewayApplePlatform: base}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))

	projectID, err := scalewayDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if projectID != "project-default" {
		t.Fatalf("projectID=%q", projectID)
	}
	if len(base.tools) != 1 || base.tools[0] != "api_key_get" || base.args[0]["access_key"] != "SCWACCESSKEY" {
		t.Fatalf("tools=%#v args=%#v", base.tools, base.args)
	}
}

func TestScalewayAppleProvisionAndDestroyUseOwnedResources(t *testing.T) {
	previousProbe := probeSSHReadyFn
	probeSSHReadyFn = func(*Instance, time.Duration) error { return nil }
	t.Cleanup(func() { probeSSHReadyFn = previousProbe })

	platform := &scalewayApplePlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	inst, err := apiProviderProvision(ctx, CreateInstanceInput{
		Name: "mac-test", Provider: "scaleway", Region: "fr-par-1",
		Size: "apple-silicon/M4-S", Image: "apple-silicon/os-macos",
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := dbGetInstance(ctx.AppDB(), inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProviderID != "mac-1" || stored.SSHUser != "m4" || stored.Platform != "macos" || stored.ResourceClass != "bare_metal" {
		t.Fatalf("stored=%#v", stored)
	}
	metadata := parseScalewayAppleMetadata(stored.ProviderMetadataJSON)
	if metadata.SSHKeyID != "key-created-by-apteva" || metadata.ProjectID != "project-1" {
		t.Fatalf("metadata=%#v", metadata)
	}
	if containsString(platform.tools, "project_list") {
		t.Fatalf("project discovery used organization-scoped project_list: %#v", platform.tools)
	}
	discoveryAt := -1
	for index, tool := range platform.tools {
		if tool == "security_group_list" {
			discoveryAt = index
			break
		}
	}
	if discoveryAt < 0 || platform.args[discoveryAt]["zone"] != "fr-par-1" || platform.args[discoveryAt]["project_default"] != true {
		t.Fatalf("project discovery args=%#v tools=%#v", platform.args, platform.tools)
	}
	if containsString(platform.tools, "server_create") || containsString(platform.tools, "server_set_cloud_init") || containsString(platform.tools, "server_action") {
		t.Fatalf("Mac provisioning used Linux instance tools: %#v", platform.tools)
	}
	createAt := -1
	for index, tool := range platform.tools {
		if tool == "apple_server_create" {
			createAt = index
			break
		}
	}
	if createAt < 0 || platform.args[createAt]["type"] != "M4-S" || platform.args[createAt]["os_id"] != "os-macos" || platform.args[createAt]["commitment_type"] != "duration_24h" {
		t.Fatalf("create args=%#v tools=%#v", platform.args, platform.tools)
	}

	stored.DeletableAt = "2020-01-01T00:00:00Z"
	if err := scalewayAppleDestroy(ctx, stored); err != nil {
		t.Fatal(err)
	}
	last := len(platform.tools)
	if last < 2 || platform.tools[last-2] != "apple_server_delete" || platform.tools[last-1] != "ssh_key_delete" {
		t.Fatalf("destroy tools=%#v", platform.tools)
	}
	if platform.args[last-2]["server_id"] != "mac-1" || platform.args[last-1]["ssh_key_id"] != "key-created-by-apteva" {
		t.Fatalf("destroy args=%#v", platform.args[last-2:])
	}
}

func TestScalewayAppleDeletionCapabilityHonorsMinimumLease(t *testing.T) {
	inst := &Instance{Provider: "scaleway", Size: "apple-silicon/M4-S", ResourceClass: "bare_metal", DeletableAt: time.Now().Add(time.Hour).Format(time.RFC3339)}
	if instanceCapabilities(inst).Destroy {
		t.Fatal("destroy capability enabled before deletable_at")
	}
	inst.DeletableAt = time.Now().Add(-time.Hour).Format(time.RFC3339)
	if !instanceCapabilities(inst).Destroy {
		t.Fatal("destroy capability disabled after deletable_at")
	}
}
