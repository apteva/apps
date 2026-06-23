package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestBuildCloudInitClearsRootPasswordExpiry(t *testing.T) {
	pubKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest apteva-test"
	userData := buildCloudInit(pubKey)

	for _, want := range []string{
		"#cloud-config",
		"ssh_authorized_keys:",
		"      - " + pubKey,
		"chpasswd:",
		"  expire: false",
		"chage -d $(date +%Y-%m-%d) -M 99999 -E -1 root",
	} {
		if !strings.Contains(userData, want) {
			t.Fatalf("cloud-init missing %q:\n%s", want, userData)
		}
	}
}

func TestParseHetznerCreateResponse_PreservesNumericID(t *testing.T) {
	id, ipv4, ipv6 := parseHetznerCreateResponse(json.RawMessage(`{
		"server": {
			"id": 12345678,
			"public_net": {
				"ipv4": {"ip": "203.0.113.10"},
				"ipv6": {"ip": "2001:db8::1"}
			}
		}
	}`))
	if id != "12345678" {
		t.Fatalf("id = %q, want 12345678", id)
	}
	if ipv4 != "203.0.113.10" || ipv6 != "2001:db8::1" {
		t.Fatalf("ips = %q/%q", ipv4, ipv6)
	}
}

func TestNormalizeHetznerID_ScientificNotation(t *testing.T) {
	if got := normalizeHetznerID("1.2345678e+07"); got != "12345678" {
		t.Fatalf("normalizeHetznerID = %q, want 12345678", got)
	}
}

type recordingDeletePlatform struct {
	tk.BasePlatformClient
	tool string
	args map[string]any
}

func (p *recordingDeletePlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"provider": float64(7)}}, nil
}

func (p *recordingDeletePlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "hetzner"}, nil
}

func (p *recordingDeletePlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.tool = tool
	p.args = input
	return &sdk.ExecuteResult{Success: true, Status: 204, Data: json.RawMessage(`null`)}, nil
}

func TestToolDestroy_NormalizesStoredHetznerIDBeforeDelete(t *testing.T) {
	platform := &recordingDeletePlatform{}
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("project-1"),
		tk.WithEmitter(rec),
		tk.WithPlatform(platform),
	)
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{
		Name: "test-1", Provider: "hetzner", ProviderID: "1.2345678e+07", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}

	app := &App{}
	res, err := app.toolDestroy(ctx, map[string]any{"id": inst.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.(map[string]any)["destroyed"] != true {
		t.Fatalf("toolDestroy result = %#v", res)
	}
	if platform.tool != "server_delete" {
		t.Fatalf("called tool %q, want server_delete", platform.tool)
	}
	if platform.args["id"] != "12345678" {
		t.Fatalf("delete id arg = %#v, want 12345678", platform.args["id"])
	}
	if _, err := dbGetInstance(ctx.AppDB(), inst.ID); err != ErrInstanceNotFound {
		t.Fatalf("dbGetInstance after destroy = %v, want ErrInstanceNotFound", err)
	}
	if events := rec.EventsByTopic(instanceDestroyedTopic); len(events) != 1 {
		t.Fatalf("destroyed events = %d, want 1", len(events))
	}
}

type recordingUpgradePlatform struct {
	tk.BasePlatformClient
	tools []string
	args  []map[string]any
}

func (p *recordingUpgradePlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"provider": float64(7)}}, nil
}

func (p *recordingUpgradePlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "hetzner"}, nil
}

func (p *recordingUpgradePlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.tools = append(p.tools, tool)
	p.args = append(p.args, input)
	switch tool {
	case "server_types_list":
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
			"server_types": [
				{"name":"cx22","cores":2,"memory":4,"disk":40,"cpu_type":"shared","architecture":"x86","deprecated":false,"prices":[{"location":"fsn1","price_monthly":{"gross":"4.51"},"price_hourly":{"gross":"0.0074"}}]},
				{"name":"cx32","cores":4,"memory":8,"disk":80,"cpu_type":"shared","architecture":"x86","deprecated":false,"prices":[{"location":"fsn1","price_monthly":{"gross":"12.18"},"price_hourly":{"gross":"0.0199"}}]}
			]
		}`)}, nil
	case "server_shutdown":
		return &sdk.ExecuteResult{Success: true, Status: 201, Data: json.RawMessage(`{"action":{"id":101,"status":"running"}}`)}, nil
	case "server_get":
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"server":{"status":"off"}}`)}, nil
	case "server_change_type":
		return &sdk.ExecuteResult{Success: true, Status: 201, Data: json.RawMessage(`{"action":{"id":102,"status":"running"}}`)}, nil
	case "server_poweron":
		return &sdk.ExecuteResult{Success: true, Status: 201, Data: json.RawMessage(`{"action":{"id":103,"status":"running"}}`)}, nil
	case "action_get":
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"action":{"status":"success"}}`)}, nil
	default:
		return &sdk.ExecuteResult{Success: false, Status: 404, Data: json.RawMessage(`{"error":"unknown tool"}`)}, nil
	}
}

func TestToolUpgrade_HetznerInPlaceSequence(t *testing.T) {
	platform := &recordingUpgradePlatform{}
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("project-1"),
		tk.WithEmitter(rec),
		tk.WithPlatform(platform),
	)
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{
		Name: "test-1", Provider: "hetzner", ProviderID: "1.2345678e+07",
		Status: "ready", Region: "fsn1", Size: "cx22",
	})
	if err != nil {
		t.Fatal(err)
	}

	app := &App{}
	res, err := app.toolUpgrade(ctx, map[string]any{
		"id":           inst.ID,
		"size":         "cx32",
		"upgrade_disk": false,
		"wait":         false,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := res.(*UpgradeInstanceResult)
	if out.OldSize != "cx22" || out.NewSize != "cx32" || out.Status != "ready" {
		t.Fatalf("upgrade result = %#v", out)
	}
	got, err := dbGetInstance(ctx.AppDB(), inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Size != "cx32" || got.Status != "ready" || got.MonthlyCostCents != 1218 {
		t.Fatalf("updated instance = %#v", got)
	}
	wantTools := []string{
		"server_types_list",
		"server_shutdown", "action_get", "server_get",
		"server_change_type", "action_get",
		"server_poweron", "action_get",
	}
	if len(platform.tools) != len(wantTools) {
		t.Fatalf("tools = %#v, want %#v", platform.tools, wantTools)
	}
	for i, want := range wantTools {
		if platform.tools[i] != want {
			t.Fatalf("tool[%d]=%q, want %q; all=%#v", i, platform.tools[i], want, platform.tools)
		}
	}
	changeArgs := map[string]any{}
	for i, tool := range platform.tools {
		if tool == "server_change_type" {
			changeArgs = platform.args[i]
			break
		}
	}
	if changeArgs["server_type"] != "cx32" || changeArgs["upgrade_disk"] != false {
		t.Fatalf("change_type args = %#v", changeArgs)
	}
	if platform.args[1]["id"] != "12345678" {
		t.Fatalf("shutdown id arg = %#v, want normalized string", platform.args[1]["id"])
	}
	if events := rec.EventsByTopic(instanceUpgradingTopic); len(events) != 1 {
		t.Fatalf("upgrading events = %d, want 1", len(events))
	}
	if events := rec.EventsByTopic(instanceUpgradedTopic); len(events) != 1 {
		t.Fatalf("upgraded events = %d, want 1", len(events))
	}
}
