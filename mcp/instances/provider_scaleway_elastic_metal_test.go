package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestElasticMetalProvisionSendsBase64UserData(t *testing.T) {
	var request map[string]any
	var publicKey string
	stop := errors.New("captured create request; do not provision")
	p := &auditPlatform{slugs: map[int64]string{7: "scaleway"}, hook: func(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		if id != 7 {
			t.Fatalf("connection = %d, want 7", id)
		}
		data := json.RawMessage(`{}`)
		switch tool {
		case "security_group_list":
			data = json.RawMessage(`{"security_groups":[{"project":"project-1"}]}`)
		case "ssh_key_create":
			publicKey, _ = args["public_key"].(string)
			data = json.RawMessage(`{"id":"key-1"}`)
		case "elastic_metal_server_create":
			request = args
			return nil, stop
		case "ssh_key_delete":
		default:
			t.Fatalf("unexpected tool %s", tool)
		}
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: data}, nil
	}}
	_, err := scalewayElasticMetalProvision(auditCtx(t, p), CreateInstanceInput{
		Name: "metal-test", ProviderConnectionID: 7, Region: "fr-par-1",
		Size: "elastic-metal/offer-1", Image: "elastic-metal/os-1", MonthlyCostCents: 100,
	})
	if !errors.Is(err, stop) || request == nil {
		t.Fatalf("create request was not captured: %v", err)
	}
	// Scaleway's CreateServerRequest.UserData is *[]byte: JSON represents it
	// as a base64 string, not a protobuf {"value": ...} wrapper object.
	encoded, ok := request["user_data"].(string)
	if !ok {
		t.Fatalf("user_data type = %T, want base64 string", request["user_data"])
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || publicKey == "" || string(decoded) != buildCloudInit(publicKey) {
		t.Fatalf("user_data did not round-trip to the registered key's cloud-init: %v", err)
	}
	if request["offer_id"] != "offer-1" || request["project_id"] != "project-1" {
		t.Fatal("incorrect offer or project")
	}
	install := request["install"].(map[string]any)
	if install["os_id"] != "os-1" || install["ssh_key_ids"].([]string)[0] != "key-1" {
		t.Fatal("incorrect installation configuration")
	}
}

func TestParseScalewayElasticMetalOffers(t *testing.T) {
	data := json.RawMessage(`{"offers":[{"id":"offer-a610r","name":"EM-A610R-NVMe","stock":"available","subscription_period":"hourly","price_per_hour":{"units":0,"nanos":120000000},"price_per_month":{"units":79,"nanos":0},"cpus":[{"name":"AMD EPYC","core_count":8,"thread_count":16}],"memories":[{"capacity":34359738368,"type":"ddr4"}],"disks":[{"capacity":1000000000000,"type":"nvme"},{"capacity":1000000000000,"type":"nvme"}]}]}`)
	types, err := parseScalewayElasticMetalOffers(data, "fr-par-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 1 {
		t.Fatalf("types=%#v", types)
	}
	got := types[0]
	if got.Name != "elastic-metal/offer-a610r" || !strings.HasPrefix(got.Description, "EM-A610R-NVMe") || got.Cores != 8 || got.MemoryGB != 32 || got.DiskGB != 2000 || got.ResourceClass != "bare_metal" {
		t.Fatalf("unexpected offer: %#v", got)
	}
	if got.HourlyPriceEUR != 0.12 || got.MonthlyPriceEUR != 79 {
		t.Fatalf("prices=%v/%v", got.HourlyPriceEUR, got.MonthlyPriceEUR)
	}
	if len(got.BootStorage) != 1 || got.BootStorage[0].StorageClass != "local" {
		t.Fatalf("storage=%#v", got.BootStorage)
	}
}

func TestElasticMetalInstanceClassification(t *testing.T) {
	inst := &Instance{Provider: "scaleway", Size: "elastic-metal/offer-a610r", ResourceClass: "bare_metal"}
	if !isScalewayElasticMetalInstance(inst) {
		t.Fatal("expected Elastic Metal instance")
	}
	if isScalewayDediboxInstance(inst) || isScalewayAppleInstance(inst) {
		t.Fatal("Elastic Metal must remain a distinct adapter")
	}
}

func TestScalewayElasticMetalRAIDLevel(t *testing.T) {
	for input, want := range map[string]string{
		"raid1":        "raid_level_1",
		"raid_level_1": "raid_level_1",
		"RAID10":       "raid_level_10",
	} {
		if got := scalewayElasticMetalRAIDLevel(input); got != want {
			t.Fatalf("scalewayElasticMetalRAIDLevel(%q)=%q, want %q", input, got, want)
		}
	}
}
