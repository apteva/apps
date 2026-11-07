package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type cloudingUpcloudTestPlatform struct {
	auditPlatform
	public map[string]string
}

func (p *cloudingUpcloudTestPlatform) GetConnectionPublicConfig(id int64) (*sdk.ConnectionPublicConfig, error) {
	return &sdk.ConnectionPublicConfig{Fields: p.public}, nil
}
func cloudingUpcloudTestCtx(t *testing.T, slug string, hook func(int64, string, map[string]any) (*sdk.ExecuteResult, error)) (*sdk.AppCtx, *cloudingUpcloudTestPlatform) {
	p := &cloudingUpcloudTestPlatform{auditPlatform: auditPlatform{slugs: map[int64]string{7: slug}, hook: hook}}
	return tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p)), p
}
func providerJSON(value string) *sdk.ExecuteResult {
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(value)}
}
func missingProviderResource() *sdk.ExecuteResult {
	return &sdk.ExecuteResult{Status: 404, Data: json.RawMessage(`{}`)}
}

func cloudingFixtures(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	switch tool {
	case "list_flavors":
		return providerJSON(`{"flavors":[{"id":"0.5x1","vCores":0.5,"ramGb":1},{"id":"2x4","vCores":2,"ramGb":4}]}`), nil
	case "list_images":
		return providerJSON(`{"images":[{"id":"ubuntu","name":"Ubuntu 24.04 (64 Bit)","minimumSizeGb":15,"accessMethods":{"sshKey":"optional","password":"optional"}},{"id":"windows","name":"Windows Server","accessMethods":{"sshKey":"not-supported"}},{"id":"private-required","name":"Debian 12","accessMethods":{"sshKey":"required-with-private-key"}},{"id":"password-required","name":"Ubuntu 24.04 panel","accessMethods":{"sshKey":"optional","password":"required"}}]}`), nil
	case "list_volume_sizes":
		return providerJSON(`{"volumeSizes":[{"storageType":"ssd","sizeGb":10},{"storageType":"ssd","sizeGb":25},{"storageType":"ssd","sizeGb":50}]}`), nil
	case "list_firewalls":
		return providerJSON(`{"values":[{"id":"http","rules":[{"enabled":true,"protocol":"tcp","portRangeMin":80,"portRangeMax":80,"sourceIp":"0.0.0.0/0"}]},{"id":"ssh","rules":[{"enabled":true,"protocol":"tcp","portRangeMin":22,"portRangeMax":22,"sourceIp":"0.0.0.0/0"}]}]}`), nil
	case "create_ssh_key":
		return providerJSON(`{"id":"owned-key"}`), nil
	}
	return nil, errors.New("unexpected tool " + tool)
}
func TestCloudingCatalogAndBootValidation(t *testing.T) {
	ctx, p := cloudingUpcloudTestCtx(t, "clouding", cloudingFixtures)
	types, err := cloudingUpcloudServerTypes(ctx, "clouding")
	if err != nil || len(types) != 2 || types[0].VCPUs != 0.5 || types[0].Cores != 0 || types[0].PriceKnown == nil || *types[0].PriceKnown {
		t.Fatalf("types=%+v err=%v", types, err)
	}
	in := CreateInstanceInput{Name: "Test Host", Provider: "clouding", ProviderConnectionID: 7, Size: "2x4", Region: "barcelona", Image: "ubuntu"}
	if err = applyAPIProviderDefaults(ctx, "clouding", &in); err != nil {
		t.Fatal(err)
	}
	if in.Storage.Boot == nil || in.Storage.Boot.SizeGB != 25 {
		t.Fatalf("boot=%+v", in.Storage.Boot)
	}
	inst, err := dbCreateInstance(ctx.AppDB(), in)
	if err != nil {
		t.Fatal(err)
	}
	tool, args, err := cloudingUpcloudCreateRequest(ctx, inst, in, "ssh-ed25519 AAAA test")
	if err != nil {
		t.Fatal(err)
	}
	if tool != "create_server" || args["hostname"] != "test-host" {
		t.Fatalf("create=%s %+v", tool, args)
	}
	if args["volume"].(map[string]any)["ssdGb"] != 25 || args["accessConfiguration"].(map[string]any)["savePassword"] != false {
		t.Fatal(args)
	}
	fresh, _ := dbGetInstance(ctx.AppDB(), inst.ID)
	if cloudingUpcloudMeta(fresh.ProviderMetadataJSON).SSHKeyID != "owned-key" {
		t.Fatal("SSH key ownership not durable")
	}
	for _, tool := range p.calls {
		if strings.Contains(tool, "firewall") && tool != "list_firewalls" {
			t.Fatalf("mutated shared firewall: %s", tool)
		}
	}
}
func TestCloudingRejectsUnavailableConfigurationsBeforeMutation(t *testing.T) {
	for _, which := range []string{"region", "size", "image", "disk", "private-key", "firewall"} {
		t.Run(which, func(t *testing.T) {
			ctx, p := cloudingUpcloudTestCtx(t, "clouding", cloudingFixtures)
			in := CreateInstanceInput{Name: "test", Provider: "clouding", ProviderConnectionID: 7, Size: "2x4", Region: "barcelona", Image: "ubuntu"}
			switch which {
			case "region":
				in.Region = "madrid"
			case "size":
				in.Size = "invented"
			case "image":
				in.Image = "windows"
			case "disk":
				in.Storage.Boot = &BootStorageRequest{SizeGB: 21, Tier: "balanced"}
			case "private-key":
				in.Image = "private-required"
			case "firewall":
				p.public = map[string]string{"firewall_id": "http"}
			}
			if err := applyAPIProviderDefaults(ctx, "clouding", &in); err == nil {
				t.Fatal("invalid input accepted")
			}
			for _, tool := range p.calls {
				if strings.HasPrefix(tool, "create_") {
					t.Fatal("mutation before validation")
				}
			}
		})
	}
}
func TestCloudingPaginationUsesNextLinkAndKeepsPageSize(t *testing.T) {
	calls := 0
	ctx, _ := cloudingUpcloudTestCtx(t, "clouding", func(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		calls++
		if args["pageSize"] != 200 {
			t.Fatal(args)
		}
		if calls == 1 {
			return providerJSON(`{"flavors":[{"id":"1x2","vCores":1,"ramGb":2}],"links":{"next":"https://api.clouding.io/v1/sizes/flavors?page=2&pagesize=200"}}`), nil
		}
		if args["page"] != "2" {
			t.Fatal(args)
		}
		return providerJSON(`{"flavors":[{"id":"2x4","vCores":2,"ramGb":4}],"links":{"next":null}}`), nil
	})
	rows, err := cloudingUpcloudServerTypes(ctx, "clouding")
	if err != nil || len(rows) != 2 || calls != 2 {
		t.Fatalf("rows=%+v calls=%d err=%v", rows, calls, err)
	}
}
func TestProviderResourceParsingRejectsActionAndPrivateAddresses(t *testing.T) {
	for _, test := range []struct{ provider, data, id, ip string }{
		{"clouding", `{"id":"server","status":"Spawning","flavor":"2x4","publicIp":"0.0.0.0","action":{"id":"action","resourceId":"server"}}`, "server", ""},
		{"clouding", `{"id":"action","resourceId":"server","status":"inProgress"}`, "", ""},
		{"clouding", `{"id":"server","status":"Active","flavor":"2x4","publicIp":"203.0.113.1"}`, "server", "203.0.113.1"},
		{"upcloud", `{"server":{"uuid":"server","zone":"es-mad1","state":"started","ip_addresses":{"ip_address":[{"access":"utility","address":"10.0.0.2"},{"access":"public","address":"203.0.113.2"}]}}}`, "server", "203.0.113.2"},
	} {
		id, ip, _ := parseProviderResource(test.provider, json.RawMessage(test.data))
		if id != test.id || ip != test.ip {
			t.Fatalf("%s got %s %s", test.provider, id, ip)
		}
	}
}
func upcloudFixtures(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	switch tool {
	case "list_plans":
		return providerJSON(`{"plans":{"plan":[{"name":"2xCPU-4GB","core_number":2,"memory_amount":4096,"storage_size":80,"storage_tier":"maxiops"},{"name":"GPU","gpu_amount":1,"storage_size":80,"storage_tier":"maxiops"},{"name":"diskless","storage_size":0}]}}`), nil
	case "list_zones":
		return providerJSON(`{"zones":{"zone":[{"id":"es-mad1","description":"Madrid #1","public":"yes"},{"id":"private","public":"no"}]}}`), nil
	case "list_images":
		return providerJSON(`{"storages":{"storage":[{"uuid":"ubuntu","title":"Ubuntu Server 24.04 LTS","type":"template","access":"public","state":"online","size":10},{"uuid":"win","title":"Windows Server","type":"template","access":"public","state":"online"}]}}`), nil
	}
	return nil, errors.New("unexpected tool " + tool)
}
func TestUpCloudCatalogCreateAndBootRecovery(t *testing.T) {
	ctx, _ := cloudingUpcloudTestCtx(t, "upcloud", upcloudFixtures)
	plans, err := cloudingUpcloudServerTypes(ctx, "upcloud")
	if err != nil || len(plans) != 1 || plans[0].DiskGB != 80 {
		t.Fatalf("plans=%+v err=%v", plans, err)
	}
	in := CreateInstanceInput{Name: "Test Madrid", Provider: "upcloud", ProviderConnectionID: 7, Size: "2xCPU-4GB", Region: "es-mad1", Image: "ubuntu"}
	if err = applyAPIProviderDefaults(ctx, "upcloud", &in); err != nil {
		t.Fatal(err)
	}
	inst, err := dbCreateInstance(ctx.AppDB(), in)
	if err != nil {
		t.Fatal(err)
	}
	tool, args, err := cloudingUpcloudCreateRequest(ctx, inst, in, "ssh-ed25519 test")
	if err != nil || tool != "create_server" {
		t.Fatal(err)
	}
	server := args["server"].(map[string]any)
	disks := server["storage_devices"].(map[string]any)["storage_device"].([]map[string]any)
	if disks[0]["size"] != 80 || server["zone"] != "es-mad1" || strings.Contains(string(mustJSON(t, args)), "private_key") {
		t.Fatal(args)
	}
	fresh, _ := dbGetInstance(ctx.AppDB(), inst.ID)
	meta := cloudingUpcloudMeta(fresh.ProviderMetadataJSON)
	if meta.BootTitle == "" || meta.BootTitle != disks[0]["title"] {
		t.Fatal("boot ownership not persisted before provider call")
	}
	body := mustJSON(t, map[string]any{"server": map[string]any{"storage_devices": map[string]any{"storage_device": []map[string]any{{"type": "disk", "storage_title": meta.BootTitle, "storage": "boot-owned"}, {"type": "disk", "storage_title": "unrelated", "storage": "other"}}}}})
	if id := upcloudOwnedBoot(body, meta.BootTitle); id != "boot-owned" {
		t.Fatal(id)
	}
}
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestCloudingDestroyVerifiesKeyOwnership(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "mismatch"}[mismatch], func(t *testing.T) {
			ctx, p := cloudingUpcloudTestCtx(t, "clouding", func(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
				switch tool {
				case "get_server", "delete_server":
					return missingProviderResource(), nil
				case "get_ssh_key":
					key := "ssh-ed25519 test"
					if mismatch {
						key = "another-key"
					}
					return providerJSON(`{"publicKey":"` + key + `"}`), nil
				case "delete_ssh_key":
					return providerJSON(`{}`), nil
				}
				t.Fatalf("unexpected %s", tool)
				return nil, nil
			})
			inst := &Instance{ID: 1, Provider: "clouding", ProviderConnectionID: 7, ProviderID: "server", SSHPublicKey: "ssh-ed25519 test", ProviderMetadataJSON: `{"clouding_ssh_key_id":"owned"}`}
			err := cloudingUpcloudDestroy(ctx, inst)
			if mismatch && err == nil || !mismatch && err != nil {
				t.Fatal(err)
			}
			for _, tool := range p.calls {
				if mismatch && tool == "delete_ssh_key" {
					t.Fatal("deleted unowned key")
				}
			}
		})
	}
}
func TestUpCloudDestroyPreservesUnrelatedStorageAndRetriesLostResponse(t *testing.T) {
	deleted, failResponse := false, true
	ctx, p := cloudingUpcloudTestCtx(t, "upcloud", func(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "get_server":
			if deleted {
				return missingProviderResource(), nil
			}
			return providerJSON(`{"server":{"state":"stopped"}}`), nil
		case "delete_server":
			if args["storages"] != 0 {
				t.Fatal("blanket storage deletion requested")
			}
			deleted = true
			if failResponse {
				failResponse = false
				return nil, errors.New("lost response")
			}
			return missingProviderResource(), nil
		case "get_volume":
			if args["volume_id"] != "boot-owned" {
				t.Fatal(args)
			}
			return providerJSON(`{"storage":{"title":"owned-token"}}`), nil
		case "delete_volume":
			if args["volume_id"] != "boot-owned" {
				t.Fatal("deleted unrelated disk")
			}
			return providerJSON(`{}`), nil
		}
		t.Fatalf("unexpected tool %s", tool)
		return nil, nil
	})
	inst := &Instance{ID: 1, Provider: "upcloud", ProviderConnectionID: 7, ProviderID: "server", ProviderMetadataJSON: `{"upcloud_boot_id":"boot-owned","upcloud_boot_title":"owned-token"}`}
	if err := cloudingUpcloudDestroy(ctx, inst); err == nil {
		t.Fatal("lost response ignored")
	}
	if err := cloudingUpcloudDestroy(ctx, inst); err != nil {
		t.Fatal(err)
	}
	deletes := 0
	for _, tool := range p.calls {
		if tool == "delete_volume" {
			deletes++
		}
	}
	if deletes != 1 {
		t.Fatal(p.calls)
	}
}
func TestUpCloudDestroyRequiresConfirmedShutdown(t *testing.T) {
	ctx, p := cloudingUpcloudTestCtx(t, "upcloud", func(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "get_server" {
			return providerJSON(`{"server":{"state":"error"}}`), nil
		}
		return nil, errors.New("unexpected mutation")
	})
	if err := cloudingUpcloudDestroy(ctx, &Instance{ID: 1, Provider: "upcloud", ProviderConnectionID: 7, ProviderID: "server", ProviderMetadataJSON: `{"upcloud_boot_id":"owned"}`}); err == nil {
		t.Fatal("unconfirmed shutdown accepted")
	}
	if len(p.calls) != 1 {
		t.Fatal(p.calls)
	}
}
func TestUpCloudObservedVolume(t *testing.T) {
	v := &InstanceVolume{Provider: "upcloud", ProviderVolumeID: "volume-1"}
	observed, err := parseObservedVolume(v, json.RawMessage(`{"storage":{"uuid":"volume-1","state":"online","size":25,"servers":{"server":["server-1"]}}}`))
	if err != nil || !observed.Ready || observed.SizeGB != 25 || len(observed.Attached) != 1 || observed.Attached[0] != "server-1" {
		t.Fatalf("observed=%+v err=%v", observed, err)
	}
	if _, err = parseObservedVolume(v, json.RawMessage(`{"storage":{"uuid":"another","state":"online"}}`)); err == nil {
		t.Fatal("volume mismatch accepted")
	}
}

func TestCloudingUpcloudProvisionLifecycleAndRestartRecovery(t *testing.T) {
	for _, provider := range []string{"clouding", "upcloud"} {
		t.Run(provider, func(t *testing.T) {
			oldProbe := probeSSHReadyFn
			probeSSHReadyFn = func(inst *Instance, _ time.Duration) error {
				if inst.SSHUser != "root" || inst.PublicIPv4 == "" || inst.SSHPrivateKey == "" {
					return errors.New("missing SSH configuration")
				}
				return nil
			}
			defer func() { probeSSHReadyFn = oldProbe }()
			title := ""
			ctx, p := cloudingUpcloudTestCtx(t, provider, func(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
				if tool == "create_server" {
					if provider == "clouding" {
						return providerJSON(`{"id":"host","flavor":"2x4","status":"Spawning"}`), nil
					}
					server := args["server"].(map[string]any)
					title = server["storage_devices"].(map[string]any)["storage_device"].([]map[string]any)[0]["title"].(string)
					return providerJSON(`{"server":{"uuid":"host","zone":"es-mad1","state":"maintenance"}}`), nil
				}
				if tool == "get_server" {
					if provider == "clouding" {
						return providerJSON(`{"id":"host","flavor":"2x4","status":"Active","publicIp":"203.0.113.10"}`), nil
					}
					return &sdk.ExecuteResult{Success: true, Status: 200, Data: mustJSON(t, map[string]any{"server": map[string]any{"uuid": "host", "zone": "es-mad1", "state": "started", "ip_addresses": map[string]any{"ip_address": []map[string]any{{"access": "public", "address": "203.0.113.11"}}}, "storage_devices": map[string]any{"storage_device": []map[string]any{{"type": "disk", "storage_title": title, "storage": "boot-owned"}}}}})}, nil
				}
				if provider == "clouding" {
					return cloudingFixtures(id, tool, args)
				}
				return upcloudFixtures(id, tool, args)
			})
			defer stopInstanceWorkers(ctx)
			in := CreateInstanceInput{Name: "test", Provider: provider, ProviderConnectionID: 7}
			if provider == "clouding" {
				in.Region = "barcelona"
				in.Size = "2x4"
			} else {
				in.Region = "es-mad1"
				in.Size = "2xCPU-4GB"
			}
			inst, err := provisionInstance(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				fresh, e := dbGetInstance(ctx.AppDB(), inst.ID)
				if e != nil {
					t.Fatal(e)
				}
				if fresh.Status == "ready" {
					inst = fresh
					break
				}
				if fresh.Status == "error" || time.Now().After(deadline) {
					t.Fatalf("readiness: %+v", fresh)
				}
				time.Sleep(5 * time.Millisecond)
			}
			// Simulate recovery of a persisted provisioning row, without issuing create again.
			if err = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"status": "provisioning"}); err != nil {
				t.Fatal(err)
			}
			reconcileAPIProviderProvisioning(ctx)
			deadline = time.Now().Add(3 * time.Second)
			for {
				fresh, _ := dbGetInstance(ctx.AppDB(), inst.ID)
				if fresh.Status == "ready" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("recovery: %+v", fresh)
				}
				time.Sleep(5 * time.Millisecond)
			}
			creates := 0
			p.mu.Lock()
			for _, tool := range p.calls {
				if tool == "create_server" {
					creates++
				}
			}
			p.mu.Unlock()
			if creates != 1 {
				t.Fatalf("recovery created %d hosts", creates)
			}
		})
	}
}
func TestCloudingUpcloudRefusesUnsupportedRetentionAndFormatting(t *testing.T) {
	for _, provider := range []string{"clouding", "upcloud"} {
		if err := applyDestroyBootPolicy(nil, &Instance{Provider: provider, StorageJSON: `{}`}, true); err == nil {
			t.Fatalf("%s accepted unsupported boot retention", provider)
		}
	}
	if _, _, err := prepareAttachedVolume(nil, &InstanceVolume{Provider: "upcloud"}, nil); err == nil {
		t.Fatal("unsafe guest formatting accepted")
	}
}

func TestUpCloudManagedDataVolumeLifecycle(t *testing.T) {
	size, attached, deleted := 25, "", false
	ctx, p := cloudingUpcloudTestCtx(t, "upcloud", func(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "create_volume":
			if args["storage"].(map[string]any)["tier"] != "maxiops" {
				t.Fatal(args)
			}
		case "attach_volume":
			attached = args["server_id"].(string)
			if args["storage_device"].(map[string]any)["storage"] != "disk-1" {
				t.Fatal(args)
			}
		case "detach_volume":
			attached = ""
		case "resize_volume":
			size = args["storage"].(map[string]any)["size"].(int)
		case "delete_volume":
			deleted = true
		case "get_volume":
			if deleted {
				return missingProviderResource(), nil
			}
		default:
			t.Fatalf("unexpected tool %s", tool)
		}
		servers := []string{}
		if attached != "" {
			servers = append(servers, attached)
		}
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: mustJSON(t, map[string]any{"storage": map[string]any{"uuid": "disk-1", "size": size, "state": "online", "servers": map[string]any{"server": servers}}})}, nil
	})
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{Name: "vm", Provider: "upcloud", ProviderConnectionID: 7, ProviderID: "server-1", Region: "es-mad1", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := createManagedVolume(ctx, CreateVolumeInput{Name: "data", Provider: "upcloud", ProviderConnectionID: 7, Region: "es-mad1", SizeGB: 25})
	if err != nil {
		t.Fatal(err)
	}
	if v.ProviderVolumeID != "disk-1" {
		t.Fatal(v)
	}
	if err = attachProviderVolume(ctx, v, inst); err != nil {
		t.Fatal(err)
	}
	v, _ = dbGetVolume(ctx.AppDB(), v.ID)
	if err = resizeProviderVolume(ctx, v, 50); err != nil {
		t.Fatal(err)
	}
	if err = detachProviderVolume(ctx, v); err != nil {
		t.Fatal(err)
	}
	v, _ = dbGetVolume(ctx.AppDB(), v.ID)
	if err = deleteProviderVolume(ctx, v); err != nil {
		t.Fatal(err)
	}
	if _, err = dbGetVolume(ctx.AppDB(), v.ID); !errors.Is(err, ErrVolumeNotFound) {
		t.Fatal("deleted volume retained in inventory", err)
	}
	if len(p.calls) < 8 {
		t.Fatal("mutations were not verified", p.calls)
	}
}
