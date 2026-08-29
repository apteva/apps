package main

import (
	"encoding/json"
	"strings"
	"testing"
)

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
