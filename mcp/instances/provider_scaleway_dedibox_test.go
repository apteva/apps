package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

const dediboxOffersFixture = `{"offers":[{
  "id":1531,"name":"Start-9-M","payment_frequency":"monthly",
  "pricing":{"currency_code":"EUR","units":39,"nanos":990000000},
  "server_info":{"stock":"available",
    "cpus":[{"name":"AMD Ryzen 5 PRO 3600","core_count":6,"thread_count":12}],
    "memories":[{"capacity":34359738368,"type":"ddr4"}],
    "disks":[{"capacity":1000000000000,"type":"nvme"},{"capacity":1000000000000,"type":"nvme"}]
  }
}]}`

func TestParseScalewayDediboxOffers(t *testing.T) {
	types, err := parseScalewayDediboxOffers(json.RawMessage(dediboxOffersFixture), "fr-par-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 1 {
		t.Fatalf("types=%#v", types)
	}
	got := types[0]
	if got.Name != "dedibox/1531" || got.Cores != 6 || got.MemoryGB != 32 || got.DiskGB != 2000 {
		t.Fatalf("hardware=%#v", got)
	}
	if got.CPUType != "dedicated" || got.ResourceClass != "bare_metal" || got.MonthlyPriceEUR != 39.99 {
		t.Fatalf("identity/pricing=%#v", got)
	}
	if !strings.Contains(got.Description, "6c/12t") || !strings.Contains(got.Description, "NVME") {
		t.Fatalf("description=%q", got.Description)
	}
}

func TestScalewayDediboxAndAppleBareMetalAreDistinct(t *testing.T) {
	dedibox := &Instance{Provider: "scaleway", Size: "dedibox/1531", Platform: "linux", ResourceClass: "bare_metal"}
	if !isScalewayDediboxInstance(dedibox) || isScalewayAppleInstance(dedibox) {
		t.Fatalf("Dedibox classification is wrong: dedibox=%v apple=%v", isScalewayDediboxInstance(dedibox), isScalewayAppleInstance(dedibox))
	}
}

type scalewayDediboxPlatform struct {
	tk.BasePlatformClient
	mu        sync.Mutex
	tools     []string
	args      []map[string]any
	installed bool
}

func (p *scalewayDediboxPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"provider": float64(7)}}, nil
}

func (p *scalewayDediboxPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "scaleway"}, nil
}

func (p *scalewayDediboxPlatform) GetConnectionPublicConfig(id int64) (*sdk.ConnectionPublicConfig, error) {
	return &sdk.ConnectionPublicConfig{ConnectionID: id, Slug: "scaleway", Fields: map[string]string{"project_id": "project-1"}}, nil
}

func (p *scalewayDediboxPlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tools = append(p.tools, tool)
	p.args = append(p.args, input)
	data := json.RawMessage(`{}`)
	status := 200
	switch tool {
	case "dedibox_offers_list":
		data = json.RawMessage(dediboxOffersFixture)
	case "ssh_key_create":
		data = json.RawMessage(`{"id":"key-dedibox"}`)
	case "dedibox_server_create":
		data = json.RawMessage(`{"id":500,"provisioning_status":"delivering"}`)
	case "dedibox_service_get":
		data = json.RawMessage(`{"id":500,"resource_id":42,"provisioning_status":"ready"}`)
	case "dedibox_server_get":
		if p.installed {
			data = json.RawMessage(`{"id":42,"status":"ready","os":{"id":24,"name":"Ubuntu 24.04"},"interfaces":[{"ips":[{"address":"203.0.113.42","version":"ipv4"}]}]}`)
		} else {
			data = json.RawMessage(`{"id":42,"status":"ready","interfaces":[{"ips":[{"address":"203.0.113.42","version":"ipv4"}]}]}`)
		}
	case "dedibox_os_list":
		data = json.RawMessage(`{"os":[{"id":24,"name":"Ubuntu","display_name":"Ubuntu 24.04 LTS","version":"24.04","arch":"amd64","type":"server","allow_ssh_keys":true}]}`)
	case "dedibox_server_install":
		p.installed = true
		data = json.RawMessage(`{"os_id":24,"status":"booting"}`)
	case "dedibox_install_get":
		data = json.RawMessage(`{"os_id":24,"status":"installed"}`)
	case "dedibox_service_delete", "ssh_key_delete":
		data = json.RawMessage(`null`)
		status = 204
	default:
		return &sdk.ExecuteResult{Success: false, Status: 404, Data: json.RawMessage(`{"error":"unexpected tool"}`)}, nil
	}
	return &sdk.ExecuteResult{Success: true, Status: status, Data: data}, nil
}

func (p *scalewayDediboxPlatform) snapshot() ([]string, []map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.tools...), append([]map[string]any(nil), p.args...)
}

func TestScalewayDediboxProvisionInstallAndDestroy(t *testing.T) {
	previousProbe := probeSSHReadyFn
	probeSSHReadyFn = func(*Instance, time.Duration) error { return nil }
	t.Cleanup(func() { probeSSHReadyFn = previousProbe })

	platform := &scalewayDediboxPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	inst, err := apiProviderProvision(ctx, CreateInstanceInput{
		Name: "dedibox-test", Provider: "scaleway", Region: "fr-par-1",
		Size: "dedibox/1531", Image: "dedibox/ubuntu-24.04",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		inst, err = dbGetInstance(ctx.AppDB(), inst.ID)
		if err != nil {
			t.Fatal(err)
		}
		if inst.Status == "ready" || inst.Status == "error" || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if inst.Status != "ready" || inst.ProviderID != "42" || inst.PublicIPv4 != "203.0.113.42" {
		t.Fatalf("stored=%#v", inst)
	}
	if inst.SSHUser != "apteva" || inst.ResourceClass != "bare_metal" || inst.MonthlyCostCents != 3999 {
		t.Fatalf("identity=%#v", inst)
	}
	metadata := parseScalewayDediboxMetadata(inst.ProviderMetadataJSON)
	if metadata.ServiceID != "500" || metadata.SSHKeyID != "key-dedibox" || metadata.ProjectID != "project-1" || !metadata.InstallStarted {
		t.Fatalf("metadata=%#v", metadata)
	}

	tools, args := platform.snapshot()
	createAt, installAt := -1, -1
	for i, tool := range tools {
		switch tool {
		case "dedibox_server_create":
			createAt = i
		case "dedibox_server_install":
			installAt = i
		}
	}
	if createAt < 0 || args[createAt]["offer_id"] != int64(1531) || args[createAt]["project_id"] != "project-1" {
		t.Fatalf("create tools=%#v args=%#v", tools, args)
	}
	if installAt < 0 || args[installAt]["os_id"] != int64(24) || args[installAt]["user_login"] != "apteva" {
		t.Fatalf("install tools=%#v args=%#v", tools, args)
	}

	if err := scalewayDediboxDestroy(ctx, inst); err != nil {
		t.Fatal(err)
	}
	tools, args = platform.snapshot()
	last := len(tools)
	if last < 2 || tools[last-2] != "dedibox_service_delete" || tools[last-1] != "ssh_key_delete" {
		t.Fatalf("destroy tools=%#v", tools)
	}
	if args[last-2]["service_id"] != int64(500) || args[last-1]["ssh_key_id"] != "key-dedibox" {
		t.Fatalf("destroy args=%#v", args[last-2:])
	}
}
