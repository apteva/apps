package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func vultrTestPlan(id, family string, metal bool) ServerType {
	key := "plans"
	cpu := `"vcpu_count":4`
	if metal {
		key = "plans_metal"
		cpu = `"cpu_cores":8,"cpu_threads":16,"cpu_model":"EPYC","disk_count":2`
	}
	rows, e := parseVultrPlans(json.RawMessage(`{"`+key+`":[{"id":"`+id+`","type":"`+family+`",`+cpu+`,"ram":32768,"disk":240,"monthly_cost":100,"hourly_cost":0.137,"locations":["cdg"],"deploy_ondemand":true}]}`), metal, "")
	if e != nil {
		panic(e)
	}
	return rows[0]
}
func TestVultrWholeCatalogNoFamilyWhitelist(t *testing.T) {
	var cloud, metal int
	p := &auditPlatform{slugs: map[int64]string{8: "vultr"}, hook: func(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		if id != 8 {
			t.Fatal("wrong account")
		}
		if _, present := args["type"]; present {
			t.Fatal("catalog whitelists a family")
		}
		var data json.RawMessage
		switch tool {
		case "list_plans":
			cloud++
			if args["cursor"] == "cloud-next" {
				data = json.RawMessage(`{"plans":[{"id":"new-family-plan","type":"future-type","disk":50,"monthly_cost":25,"locations":["cdg"]}]}`)
			} else {
				data = json.RawMessage(`{"plans":[{"id":"gpu-plan","type":"vcg","disk":100,"monthly_cost":50,"locations":["cdg"],"gpu_count":4,"gpu_type":"NVIDIA_H100","gpu_brand":"NVIDIA","gpu_vram_gb":320},{"id":"diskless-placeholder","type":"vx1","disk":1,"storage_type":"block_storage","monthly_cost":10,"locations":["cdg"]}],"meta":{"links":{"next":"https://api.vultr.com/v2/plans?cursor=cloud-next"}}}`)
			}
		case "list_metal_plans":
			metal++
			if args["cursor"] == "metal-next" {
				data = json.RawMessage(`{"plans_metal":[{"id":"grace","type":"NVMe","cpu_model":"Grace Neoverse V2","cpu_count":72,"ram":491520,"disk":960,"monthly_cost":2000,"locations":[],"deploy_ondemand":false,"deploy_preemptible":true}]}`)
			} else {
				data = json.RawMessage(`{"plans_metal":[{"id":"metal-plan","type":"NVMe","cpu_cores":8,"cpu_threads":16,"disk":240,"disk_count":2,"monthly_cost":100,"locations":["cdg"]}],"meta":{"links":{"next":"https://api.vultr.com/v2/plans-metal?cursor=metal-next"}}}`)
			}
		default:
			t.Fatal(tool)
		}
		return &sdk.ExecuteResult{Success: true, Data: data}, nil
	}}
	ctx, release := scopedCatalog(auditCtx(t, p), catalogOptions{ConnectionID: 8})
	defer release()
	rows, e := vultrServerTypes(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 5 || cloud != 2 || metal != 2 {
		t.Fatal("incomplete paged catalog", len(rows), cloud, metal)
	}
	gpu := rows[0].Accelerators
	if len(gpu) != 1 || gpu[0].Count != 4 || gpu[0].MemoryGB != 80 {
		t.Fatal(gpu)
	}
	if rows[1].DiskGB != 0 || !rows[1].RequiresBootStorage {
		t.Fatal("placeholder disk treated as local", rows[1])
	}
	if rows[2].ProviderFamily != "future-type" {
		t.Fatal("future family lost")
	}
	if rows[3].ResourceClass != "bare_metal" || rows[3].DiskGB != 480 || rows[3].Cores != 8 {
		t.Fatal(rows[3])
	}
	if rows[4].Architecture != "arm" || *rows[4].Available || !strings.Contains(rows[4].UnavailableReason, "preemptible") {
		t.Fatal("unavailable/ARM metadata lost", rows[4])
	}
	again, e := vultrServerTypes(ctx)
	if e != nil || len(again) != 5 || cloud != 2 || metal != 2 {
		t.Fatal("catalog not cached per account")
	}
}
func TestVultrMetadataRoutesMetalWithoutSKUConvention(t *testing.T) {
	in := CreateInstanceInput{Name: "metal", Size: "next-physical-product", Image: "2284", Region: "cdg"}
	if e := configureVultrPlan(&in, vultrTestPlan(in.Size, "NVMe", true)); e != nil {
		t.Fatal(e)
	}
	tool, args, e := vultrCreateRequest(in, "ssh-ed25519 public-test")
	if e != nil || tool != "create_bare_metal" || args["user_scheme"] != "root" {
		t.Fatal(tool, args, e)
	}
	if _, present := args["block_devices"]; present {
		t.Fatal("VM storage sent to metal API")
	}
	init, e := base64.StdEncoding.DecodeString(args["user_data"].(string))
	if e != nil || !strings.Contains(string(init), "public-test") {
		t.Fatal("SSH cloud-init not delivered")
	}
	p := &auditPlatform{slugs: map[int64]string{7: "vultr"}, hook: func(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		if id != 7 || tool != "delete_bare_metal" || args["baremetal_id"] != "owned-id" {
			t.Fatal(id, tool, args)
		}
		return &sdk.ExecuteResult{Success: true}, nil
	}}
	ctx := auditCtx(t, p)
	inst := &Instance{Provider: "vultr", ProviderConnectionID: 7, ProviderID: "owned-id", Size: in.Size, ResourceClass: in.ResourceClass, ProviderMetadataJSON: in.ProviderMetadataJSON}
	tool, args, e = apiProviderGetRequest(inst)
	if e != nil || tool != "get_bare_metal" || args["baremetal_id"] != "owned-id" {
		t.Fatal(tool, args, e)
	}
	if e = apiProviderDestroy(ctx, inst); e != nil {
		t.Fatal(e)
	}
	id, ip, _ := parseProviderResource("vultr", json.RawMessage(`{"bare_metal":{"id":"owned-id","plan":"new-physical","main_ip":"203.0.113.9"}}`))
	if id != "owned-id" || ip != "203.0.113.9" {
		t.Fatal(id, ip)
	}
}
func TestVultrDisklessBootAndFixedLocalValidation(t *testing.T) {
	p := vultrTestPlan("vx1-any-new-size", "vx1", false)
	p.DiskGB = 0
	p.RequiresBootStorage = true
	p.BootStorage = p.BootStorage[1:]
	in := CreateInstanceInput{Name: "diskless", Size: p.Name, Image: "2284", Region: "cdg"}
	if e := configureVultrPlan(&in, p); e == nil {
		t.Fatal("diskless plan permitted without explicit boot choice")
	}
	in.Storage.Boot = &BootStorageRequest{SizeGB: 80, StorageClass: "block", Tier: "performance", DeletePolicy: "retain"}
	if e := configureVultrPlan(&in, p); e != nil {
		t.Fatal(e)
	}
	tool, args, e := vultrCreateRequest(in, "public-key")
	if e != nil || tool != "create_instance" {
		t.Fatal(tool, e)
	}
	devices := args["block_devices"].([]map[string]any)
	if len(devices) != 1 || devices[0]["disk_size"] != 80 || devices[0]["bootable"] != true || devices[0]["label"] != vultrMeta(in.ProviderMetadataJSON).BootLabel {
		t.Fatal(devices)
	}
	in.Storage.Boot.Tier = "balanced"
	if e = configureVultrPlan(&in, p); e == nil {
		t.Fatal("unbootable storage tier accepted")
	}
	fixed := vultrTestPlan("future-local-family", "next-type", false)
	if e = configureVultrPlan(&in, fixed); e == nil {
		t.Fatal("fixed plan accepts resizable boot override")
	}
	if e = applyDestroyBootPolicy(nil, &Instance{Provider: "vultr", StorageJSON: "{}"}, true); e == nil {
		t.Fatal("local boot retention silently ignored")
	}
}
func TestVultrRegionalQuoteAndLiveStockBeforeMutation(t *testing.T) {
	for _, stock := range []bool{true, false} {
		t.Run(map[bool]string{true: "stock", false: "no-stock"}[stock], func(t *testing.T) {
			p := &auditPlatform{slugs: map[int64]string{7: "vultr"}, hook: func(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
				var data string
				switch tool {
				case "list_plans":
					data = `{"plans":[{"id":"future-plan","type":"new-type","disk":40,"monthly_cost":10,"locations":["sao"],"location_cost":{"sao":{"monthly_cost":15,"hourly_cost":0.02}}}]}`
				case "list_metal_plans":
					data = `{"plans_metal":[]}`
				case "list_os":
					data = `{"os":[{"id":2284,"family":"ubuntu","arch":"x64","name":"Ubuntu 24.04"}]}`
				case "list_region_availability":
					if args["region_id"] != "sao" {
						t.Fatal(args)
					}
					data = `{"available_plans":[]}`
					if stock {
						data = `{"available_plans":["future-plan"]}`
					}
				default:
					t.Fatal("unexpected mutation", tool)
				}
				return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(data)}, nil
			}}
			ctx, release := scopedCatalog(auditCtx(t, p), catalogOptions{ConnectionID: 7})
			defer release()
			in := CreateInstanceInput{Size: "future-plan", Region: "sao", Image: "2284", ProviderConnectionID: 7, MonthlyCostCents: 1}
			e := applyAPIProviderDefaults(ctx, "vultr", &in)
			if stock && (e != nil || in.MonthlyCostCents != 1500) {
				t.Fatal(in.MonthlyCostCents, e)
			}
			if !stock && e == nil {
				t.Fatal("out-of-stock plan accepted")
			}
		})
	}
}
func TestVultrOwnedBootStorageCleanupAndRetention(t *testing.T) {
	for _, retain := range []bool{false, true} {
		t.Run(map[bool]string{true: "retain", false: "delete"}[retain], func(t *testing.T) {
			var deletes int
			gone := false
			p := &auditPlatform{slugs: map[int64]string{7: "vultr"}, hook: func(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
				if tool == "get_instance" {
					return &sdk.ExecuteResult{Success: false, Status: 404}, nil
				}
				if tool == "block_storage_delete" {
					if args["block_id"] != "owned-block" {
						t.Fatal("foreign volume deletion", args)
					}
					deletes++
					gone = true
					return &sdk.ExecuteResult{Success: true}, nil
				}
				if tool == "block_storage_get" {
					if gone {
						return &sdk.ExecuteResult{Success: false, Status: 404}, nil
					}
					return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"block_storage":{"id":"owned-block","label":"owned-label","region":"cdg","size_gb":80,"bootable":true,"block_type":"high_perf","status":"active","attached_to_instance":""}}`)}, nil
				}
				if tool == "block_storage_list" {
					return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"blocks":[{"id":"foreign","label":"foreign-label","region":"cdg","size_gb":80,"bootable":true,"block_type":"high_perf","attached_to_instance":"server"},{"id":"owned-block","label":"owned-label","region":"cdg","size_gb":80,"bootable":true,"block_type":"high_perf","attached_to_instance":"server"}]}`)}, nil
				}
				return nil, errors.New("unexpected " + tool)
			}}
			ctx := auditCtx(t, p)
			policy := "with_instance"
			if retain {
				policy = "retain"
			}
			i, e := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{Name: "boot", Provider: "vultr", ProviderConnectionID: 7, ProviderID: "server", Region: "cdg", Status: "destroying", ProviderMetadataJSON: nativeJSON(vultrMetadata{Kind: "instance", BootLabel: "owned-label"}), Storage: InstanceStorageRequest{Boot: &BootStorageRequest{SizeGB: 80, StorageClass: "block", Tier: "performance", ProviderType: "high_perf", DeletePolicy: policy}}})
			if e != nil {
				t.Fatal(e)
			}
			if e = reconcileVultrBootStorage(ctx, i); e != nil {
				t.Fatal(e)
			}
			volumes, e := dbListVolumes(ctx.AppDB(), i.ID, "")
			if e != nil || len(volumes) != 1 || volumes[0].ProviderVolumeID != "owned-block" {
				t.Fatal(volumes, e)
			}
			if e = finalizeVultrBootStorage(ctx, i); e != nil {
				t.Fatal(e)
			}
			if retain && deletes != 0 || !retain && deletes != 1 {
				t.Fatal("wrong delete policy", deletes)
			}
			if e = finalizeVultrBootStorage(ctx, i); e != nil {
				t.Fatal("cleanup retry", e)
			}
			if !retain && deletes != 1 {
				t.Fatal("cleanup replayed deletion")
			}
		})
	}
}
func TestVultrPublicCatalogReadOnly(t *testing.T) {
	if os.Getenv("INSTANCES_VULTR_PUBLIC_CATALOG_TEST") != "1" {
		t.Skip("opt-in public GET catalog smoke test")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	for _, metal := range []bool{false, true} {
		path := "plans"
		key := "plans"
		if metal {
			path = "plans-metal"
			key = "plans_metal"
		}
		resp, e := client.Get("https://api.vultr.com/v2/" + path + "?per_page=500")
		if e != nil {
			t.Fatal(e)
		}
		var raw map[string]json.RawMessage
		e = json.NewDecoder(resp.Body).Decode(&raw)
		resp.Body.Close()
		if e != nil || resp.StatusCode != 200 {
			t.Fatal(resp.Status, e)
		}
		var source []json.RawMessage
		json.Unmarshal(raw[key], &source)
		body, _ := json.Marshal(raw)
		rows, e := parseVultrPlans(body, metal, "")
		if e != nil || len(rows) != len(source) || len(rows) == 0 {
			t.Fatal("dropped catalog entries", len(source), len(rows), e)
		}
		t.Logf("%s: %d plans preserved, including unavailable entries", path, len(rows))
	}
}

func TestVultrBootCleanupRefusesForeignOrUnconfirmedResources(t *testing.T) {
	for _, mode := range []string{"parent-unconfirmed", "foreign-label", "foreign-attachment"} {
		t.Run(mode, func(t *testing.T) {
			p := &auditPlatform{slugs: map[int64]string{7: "vultr"}, hook: func(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
				if tool == "get_instance" {
					if mode == "parent-unconfirmed" {
						return &sdk.ExecuteResult{Success: false, Status: 500}, nil
					}
					return &sdk.ExecuteResult{Success: false, Status: 404}, nil
				}
				if tool == "block_storage_delete" {
					t.Fatal("unsafe deletion")
				}
				if tool == "block_storage_get" {
					label := "owned-label"
					attached := ""
					if mode == "foreign-label" {
						label = "foreign"
					}
					if mode == "foreign-attachment" {
						attached = "other-server"
					}
					return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"block_storage":{"id":"block","label":"` + label + `","attached_to_instance":"` + attached + `","region":"cdg","bootable":true}}`)}, nil
				}
				t.Fatal(tool)
				return nil, nil
			}}
			ctx := auditCtx(t, p)
			i, e := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{Provider: "vultr", ProviderConnectionID: 7, ProviderID: "server", Status: "destroying", Region: "cdg", ProviderMetadataJSON: nativeJSON(vultrMetadata{Kind: "instance", BootLabel: "owned-label"}), Storage: InstanceStorageRequest{Boot: &BootStorageRequest{SizeGB: 80, StorageClass: "block", DeletePolicy: "with_instance"}}})
			if e != nil {
				t.Fatal(e)
			}
			if e = persistManagedBootVolume(ctx.AppDB(), i, &scalewayBootVolume{ID: "block", SizeGB: 80, StorageClass: "block", ProviderType: "high_perf"}, &BootStorageRequest{DeletePolicy: "with_instance"}); e != nil {
				t.Fatal(e)
			}
			if e = finalizeVultrBootStorage(ctx, i); e == nil {
				t.Fatal("unverified resource accepted")
			}
			rows, e := dbListVolumes(ctx.AppDB(), i.ID, "")
			if e != nil || len(rows) != 1 {
				t.Fatal("unverified inventory discarded")
			}
		})
	}
}

func TestVultrExplicitFreeAndMissingRegionalPricesDiffer(t *testing.T) {
	for _, tc := range []struct {
		price, region string
		known         bool
	}{{`"monthly_cost":0`, "", true}, {`"hourly_cost":0.1`, "", false}, {`"monthly_cost":10,"location_cost":{"cdg":{"hourly_cost":0.1}}`, "cdg", false}} {
		rows, e := parseVultrPlans(json.RawMessage(`{"plans":[{"id":"price-test","disk":25,"locations":["cdg"],`+tc.price+`}]}`), false, tc.region)
		if e != nil || len(rows) != 1 || *rows[0].PriceKnown != tc.known || *rows[0].Available != tc.known {
			t.Fatal("unknown price treated as free", rows, e)
		}
	}
}
func TestVultrNewBlockFamilyUsesStorageMetadata(t *testing.T) {
	rows, e := parseVultrPlans(json.RawMessage(`{"plans":[{"id":"new-product-no-vx1-prefix","type":"future","storage_type":"block_storage","disk":1,"monthly_cost":20,"locations":["cdg"]}]}`), false, "")
	if e != nil {
		t.Fatal(e)
	}
	in := CreateInstanceInput{Size: rows[0].Name, Image: "2284", Storage: InstanceStorageRequest{Boot: &BootStorageRequest{SizeGB: 80, StorageClass: "block", Tier: "performance"}}}
	if e = configureVultrPlan(&in, rows[0]); e != nil {
		t.Fatal(e)
	}
	_, args, e := vultrCreateRequest(in, "public-key")
	if e != nil || len(args["block_devices"].([]map[string]any)) != 1 {
		t.Fatal("new catalog family lost provider boot protocol", args, e)
	}
}

func TestVultrBootDeleteLostResponseRecoversWithoutOrphanOperation(t *testing.T) {
	gone := false
	deletes := 0
	p := &auditPlatform{slugs: map[int64]string{7: "vultr"}, hook: func(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "get_instance" || tool == "block_storage_get" && gone {
			return &sdk.ExecuteResult{Success: false, Status: 404}, nil
		}
		if tool == "block_storage_get" {
			return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"block_storage":{"id":"block","label":"owned-label","region":"cdg","bootable":true,"size_gb":80,"status":"active","attached_to_instance":""}}`)}, nil
		}
		if tool == "block_storage_delete" {
			deletes++
			gone = true
			return nil, errors.New("response lost after deletion")
		}
		t.Fatal(tool)
		return nil, nil
	}}
	ctx := auditCtx(t, p)
	boot := &BootStorageRequest{SizeGB: 80, StorageClass: "block", DeletePolicy: "with_instance"}
	i, e := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{Provider: "vultr", ProviderConnectionID: 7, ProviderID: "server", Region: "cdg", Status: "destroying", ProviderMetadataJSON: nativeJSON(vultrMetadata{Kind: "instance", BootLabel: "owned-label"}), Storage: InstanceStorageRequest{Boot: boot}})
	if e != nil {
		t.Fatal(e)
	}
	if e = persistManagedBootVolume(ctx.AppDB(), i, &scalewayBootVolume{ID: "block", SizeGB: 80, StorageClass: "block", ProviderType: "high_perf"}, boot); e != nil {
		t.Fatal(e)
	}
	if e = finalizeVultrBootStorage(ctx, i); e == nil {
		t.Fatal("lost response was not marked unknown")
	}
	if e = finalizeVultrBootStorage(ctx, i); e != nil {
		t.Fatal(e)
	}
	if deletes != 1 {
		t.Fatal("delete replayed", deletes)
	}
	var pending int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM resource_operations`).Scan(&pending)
	if pending != 0 {
		t.Fatal("orphan operation", pending)
	}
}

func TestVultrOpaquePlanIDCannotOverridePersistedResourceKind(t *testing.T) {
	i := &Instance{Provider: "vultr", ProviderID: "id", Size: "vbm-looking-virtual", ProviderMetadataJSON: nativeJSON(vultrMetadata{Kind: "instance"})}
	tool, _, e := apiProviderGetRequest(i)
	if e != nil || tool != "get_instance" {
		t.Fatal("plan naming overrode endpoint metadata", tool, e)
	}
	i.Size = "opaque-physical"
	i.ProviderMetadataJSON = nativeJSON(vultrMetadata{Kind: "bare_metal"})
	tool, _, e = apiProviderGetRequest(i)
	if e != nil || tool != "get_bare_metal" {
		t.Fatal(tool, e)
	}
}
