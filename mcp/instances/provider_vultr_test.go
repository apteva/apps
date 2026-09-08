package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"golang.org/x/crypto/ssh"
)

const vx1Plan = `{"id":"vx1-g-2c-8g-120s","type":"vx1","vcpu_count":2,"ram":8192,"disk":120,"monthly_cost":55.48,"hourly_cost":0.076,"locations":["cdg"],"deploy_ondemand":true,"storage_type":"local_and_block_storage"}`

func vultrCatalogFixture(tool string, args map[string]any) json.RawMessage {
	if tool == "list_os" {
		return json.RawMessage(`{"os":[{"id":2284,"name":"Ubuntu 24.04 LTS x64","family":"ubuntu","arch":"x64"}]}`)
	}
	if args["type"] == "vc2" {
		return json.RawMessage(`{"plans":[{"id":"vc2-1c-1gb","type":"vc2","disk":25,"monthly_cost":5,"locations":["cdg"]}]}`)
	}
	if args["cursor"] == "next-vx1" {
		return json.RawMessage(`{"plans":[` + vx1Plan + `],"meta":{"links":{"next":""}}}`)
	}
	return json.RawMessage(`{"plans":[{"id":"vx1-diskless","type":"vx1","disk":0,"monthly_cost":40,"locations":["cdg"]},{"id":"vx1-disabled","type":"vx1","disk":120,"locations":["cdg"],"deploy_ondemand":false},{"id":"vx1-unavailable","type":"vx1","disk":120,"locations":[]}],"meta":{"links":{"next":"https://api.vultr.com/v2/plans?cursor=next-vx1"}}}`)
}

func TestVultrCatalogIncludesPagedVX1AndProviderPrice(t *testing.T) {
	p := &auditPlatform{slugs: map[int64]string{7: "vultr", 8: "vultr"}, hook: func(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		if id != 8 {
			t.Fatalf("queried connection %d, want selected account 8", id)
		}
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: vultrCatalogFixture(tool, args)}, nil
	}}
	ctx, release := scopedCatalog(auditCtx(t, p), catalogOptions{ConnectionID: 8})
	defer release()
	types, err := apiProviderListServerTypes(ctx, "vultr")
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 2 {
		t.Fatalf("catalog=%+v", types)
	}
	plan := types[1]
	if plan.Name != "vx1-g-2c-8g-120s" || plan.CPUType != "dedicated" || plan.MonthlyPriceUSD != 55.48 || plan.HourlyPriceUSD != 0.076 || len(plan.BootStorage) != 1 || plan.BootStorage[0].StorageClass != "local" {
		t.Fatalf("incorrect VX1 contract: %+v", plan)
	}
	in := CreateInstanceInput{Size: plan.Name, Region: "cdg", Image: "2284", ProviderConnectionID: 8, MonthlyCostCents: 1}
	if err := applyAPIProviderDefaults(ctx, "vultr", &in); err != nil {
		t.Fatal(err)
	}
	if in.MonthlyCostCents != 5548 {
		t.Fatalf("cost=%d, want 5548", in.MonthlyCostCents)
	}
	in.Region = "ewr"
	if err := applyAPIProviderDefaults(ctx, "vultr", &in); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("region validation=%v", err)
	}
}

func TestVultrRejectsUnpricedUnknownAndDisklessBeforePurchase(t *testing.T) {
	for _, name := range []string{"unknown", "vx1-diskless", "vx1-no-price", "catalog-failure"} {
		t.Run(name, func(t *testing.T) {
			p := &auditPlatform{slugs: map[int64]string{7: "vultr"}, hook: func(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
				if tool == "create_instance" {
					t.Fatal("made paid mutation for an invalid plan")
				}
				if name == "catalog-failure" {
					return nil, fmt.Errorf("catalog unavailable")
				}
				data := vultrCatalogFixture(tool, args)
				if name == "vx1-no-price" {
					data = json.RawMessage(`{"plans":[{"id":"vx1-no-price","type":"vx1","disk":120,"locations":["cdg"]}]}`)
				}
				return &sdk.ExecuteResult{Success: true, Status: 200, Data: data}, nil
			}}
			_, err := provisionInstance(auditCtx(t, p), CreateInstanceInput{Name: "reject", Provider: "vultr", ProviderConnectionID: 7, Size: name, Region: "cdg", Image: "2284"})
			if err == nil {
				t.Fatal("expected catalog validation error")
			}
		})
	}
}

func TestVultrPlaceholderAddressParsing(t *testing.T) {
	for _, addresses := range []string{`"main_ip":"0.0.0.0","v6_main_ip":"::"`, `"main_ip":"","v6_main_ip":"0:0:0:0:0:0:0:0"`, `"main_ip":"not-an-address"`} {
		id, v4, v6 := parseProviderResource("vultr", json.RawMessage(`{"instance":{"id":"server-1","plan":"vx1",`+addresses+`}}`))
		if id != "server-1" || v4 != "" || v6 != "" {
			t.Fatalf("placeholder parsed as %q %q %q", id, v4, v6)
		}
	}
}

func TestVultrReadinessRefreshesPlaceholderBeforeSSH(t *testing.T) {
	for _, persisted := range []bool{false, true} {
		t.Run(fmt.Sprintf("persisted=%v", persisted), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "cloud-init"), []byte("#!/bin/sh\necho 'status: done'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			client := auditSSHClient(t, dir)
			oldDial, oldProbe := dialAdministrativeSSH, probeSSHReadyFn
			dialAdministrativeSSH = func(*Instance, time.Duration) (*ssh.Client, error) { return client, nil }
			probed := make(chan string, 1)
			probeSSHReadyFn = func(inst *Instance, _ time.Duration) error { probed <- inst.PublicIPv4; return nil }
			defer func() { dialAdministrativeSSH = oldDial; probeSSHReadyFn = oldProbe }()
			var lookups atomic.Int32
			p := &auditPlatform{slugs: map[int64]string{7: "vultr"}, hook: func(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
				data := vultrCatalogFixture(tool, args)
				if tool == "create_instance" || (tool == "get_instance" && lookups.Add(1) == 1) {
					data = json.RawMessage(`{"instance":{"id":"server-1","plan":"vx1","main_ip":"0.0.0.0","v6_main_ip":"::"}}`)
				} else if tool == "get_instance" {
					data = json.RawMessage(`{"instance":{"id":"server-1","plan":"vx1","main_ip":"203.0.113.47"}}`)
				}
				return &sdk.ExecuteResult{Success: true, Status: 200, Data: data}, nil
			}}
			ctx := auditCtx(t, p)
			defer stopInstanceWorkers(ctx)
			var inst *Instance
			var err error
			if persisted {
				inst, err = dbCreateInstance(ctx.AppDB(), CreateInstanceInput{Name: "old-record", Provider: "vultr", ProviderConnectionID: 7, ProviderID: "server-1", PublicIPv4: "0.0.0.0", PublicIPv6: "::", Status: "provisioning"})
				if err == nil {
					kickAPIProviderReadinessProbe(ctx, inst.ID)
				}
			} else {
				inst, err = provisionInstance(ctx, CreateInstanceInput{Name: "new-record", Provider: "vultr", ProviderConnectionID: 7, Size: "vx1-g-2c-8g-120s", Region: "cdg", Image: "2284"})
			}
			if err != nil {
				t.Fatal(err)
			}
			ready, err := waitInstanceReady(context.Background(), ctx, inst.ID, 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if ready.PublicIPv4 != "203.0.113.47" || ready.PublicIPv6 != "" || lookups.Load() != 2 {
				t.Fatalf("network=%s/%s lookups=%d", ready.PublicIPv4, ready.PublicIPv6, lookups.Load())
			}
			if got := <-probed; got != "203.0.113.47" {
				t.Fatalf("SSH probed placeholder %q", got)
			}
			if !persisted && ready.MonthlyCostCents != 5548 {
				t.Fatalf("cost=%d", ready.MonthlyCostCents)
			}
		})
	}
}
