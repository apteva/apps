package main

import (
	"encoding/json"
	"testing"
)

func TestActiveServerTypes_FiltersDeprecated(t *testing.T) {
	types, err := parseHetznerServerTypes(json.RawMessage(`{
		"server_types": [
			{"name":"old","cores":2,"memory":4,"disk":40,"deprecated":true,"prices":[]},
			{"name":"current","cores":4,"memory":8,"disk":80,"deprecated":false,"prices":[]}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	active := activeServerTypes(types)
	if len(active) != 1 || active[0].Name != "current" {
		t.Fatalf("active server types = %#v, want only current", active)
	}
}

func TestParseDigitalOceanSizes(t *testing.T) {
	types, err := parseDigitalOceanSizes(json.RawMessage(`{
		"sizes": [
			{"slug":"s-1vcpu-512mb-10gb","memory":512,"vcpus":1,"disk":10,"price_monthly":4,"price_hourly":0.006,"regions":["nyc1"],"available":true,"description":"Basic"},
			{"slug":"old","memory":512,"vcpus":1,"disk":10,"price_monthly":1,"regions":["nyc1"],"available":false}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 1 {
		t.Fatalf("types = %d, want 1", len(types))
	}
	got := types[0]
	if got.Name != "s-1vcpu-512mb-10gb" || got.Cores != 1 || got.MemoryGB != 0.5 || got.MonthlyPriceUSD != 4 {
		t.Fatalf("type = %#v", got)
	}
	if len(got.AvailableIn) != 1 || got.AvailableIn[0] != "nyc1" {
		t.Fatalf("available_in = %#v", got.AvailableIn)
	}
}

func TestParseDigitalOceanRegionsAndImages(t *testing.T) {
	locs, err := parseDigitalOceanRegions(json.RawMessage(`{
		"regions": [
			{"slug":"nyc1","name":"New York 1","available":true},
			{"slug":"off","name":"Off","available":false}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 1 || locs[0].Name != "nyc1" || locs[0].City != "New" {
		t.Fatalf("locations = %#v", locs)
	}

	imgs, err := parseDigitalOceanImages(json.RawMessage(`{
		"images": [
			{"id":123,"name":"24.04 x64","slug":"ubuntu-24-04-x64","distribution":"Ubuntu","public":true,"type":"base","min_disk_size":10},
			{"id":456,"name":"Snapshot","distribution":"Ubuntu","public":false,"type":"snapshot"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 || imgs[0].Name != "ubuntu-24-04-x64" || imgs[0].OSFlavor != "ubuntu" || imgs[0].OSVersion != "24.04" {
		t.Fatalf("images = %#v", imgs)
	}
}

func TestParseDigitalOceanDropletResponse(t *testing.T) {
	id, ipv4, ipv6 := parseDigitalOceanDropletResponse(json.RawMessage(`{
		"droplet": {
			"id": 12345,
			"networks": {
				"v4": [{"ip_address":"10.0.0.2","type":"private"},{"ip_address":"203.0.113.9","type":"public"}],
				"v6": [{"ip_address":"2001:db8::9","type":"public"}]
			}
		}
	}`))
	if id != "12345" || ipv4 != "203.0.113.9" || ipv6 != "2001:db8::9" {
		t.Fatalf("droplet = %q %q %q", id, ipv4, ipv6)
	}
}
