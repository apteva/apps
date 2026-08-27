package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestAPIProviderAdaptersAreCompatible(t *testing.T) {
	for _, provider := range apiProviderSlugs {
		if !isCompatibleProvider(provider) || !isAPIProvider(provider) {
			t.Fatalf("provider %q is not wired through both compatibility and adapter dispatch", provider)
		}
		cap := instanceCapabilities(&Instance{Provider: provider})
		wantDestroy := provider != "contabo"
		if cap.Destroy != wantDestroy {
			t.Fatalf("provider %q destroy capability = %v, want %v", provider, cap.Destroy, wantDestroy)
		}
	}
}

func TestParseAPIProviderServerTypes(t *testing.T) {
	tests := []struct {
		provider string
		data     string
		name     string
		cores    int
		memory   float64
	}{
		{"vultr", `{"plans":[{"id":"vc2-1c-1gb","vcpu_count":1,"ram":1024,"disk":25,"monthly_cost":5,"hourly_cost":0.007,"locations":["ams"]}]}`, "vc2-1c-1gb", 1, 1},
		{"linode", `{"data":[{"id":"g6-standard-1","label":"Linode 2 GB","class":"standard","vcpus":1,"memory":2048,"disk":51200,"price":{"hourly":0.018,"monthly":12}}]}`, "g6-standard-1", 1, 2},
		{"scaleway", `{"servers":{"DEV1-S":{"ncpus":2,"ram":2147483648,"arch":"x86_64","hourly_price":{"units":0,"nanos":9000000},"monthly_price":6.42,"volumes_constraint":{"min_size":20000000000}}}}`, "DEV1-S", 2, 2},
		{"huawei-cloud", `{"flavors":[{"id":"s6.small.1","name":"s6.small.1","vcpus":"1","ram":1024,"disk":0}]}`, "s6.small.1", 1, 1},
		{"ovhcloud", `[{"id":"flavor-1","name":"b2-7","vcpus":2,"ram":7000,"disk":50,"monthlyPrice":{"value":20},"hourlyPrice":{"value":0.03}}]`, "flavor-1", 2, 6.8359375},
		{"aws-ec2", `{"DescribeInstanceTypesResponse":{"instanceTypeSet":{"item":{"instanceType":"t3.micro","vCpuInfo":{"defaultVCpus":"2"},"memoryInfo":{"sizeInMiB":"1024"},"processorInfo":{"supportedArchitectures":{"item":"x86_64"}}}}}}`, "t3.micro", 2, 1},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			types, err := parseProviderServerTypes(tt.provider, json.RawMessage(tt.data))
			if err != nil {
				t.Fatal(err)
			}
			if len(types) != 1 || types[0].Name != tt.name || types[0].Cores != tt.cores || types[0].MemoryGB != tt.memory {
				t.Fatalf("types = %#v", types)
			}
		})
	}
}

func TestParseAPIProviderLocationsAndImages(t *testing.T) {
	locs, err := parseProviderLocations("aws-ec2", json.RawMessage(`{"availabilityZoneInfo":{"item":{"zoneName":"eu-west-1a","zoneState":"available","regionName":"eu-west-1"}}}`))
	if err != nil || len(locs) != 1 || locs[0].Name != "eu-west-1a" {
		t.Fatalf("AWS locations = %#v, err=%v", locs, err)
	}
	locs, err = parseProviderLocations("huawei-cloud", json.RawMessage(`{"availabilityZoneInfo":[{"zoneName":"eu-west-0a","zoneState":{"available":true}}]}`))
	if err != nil || len(locs) != 1 || locs[0].Name != "eu-west-0a" {
		t.Fatalf("Huawei locations = %#v, err=%v", locs, err)
	}

	images, err := parseProviderImages("contabo", json.RawMessage(`{"data":[{"imageId":"image-1","name":"Ubuntu 24.04","osType":"Linux"}]}`))
	if err != nil || len(images) != 1 || images[0].Name != "image-1" {
		t.Fatalf("Contabo images = %#v, err=%v", images, err)
	}
	images, err = parseProviderImages("ovhcloud", json.RawMessage(`[{"id":"image-2","name":"Ubuntu 24.04","type":"linux","status":"active","minDisk":10}]`))
	if err != nil || len(images) != 1 || images[0].Name != "image-2" {
		t.Fatalf("OVHcloud images = %#v, err=%v", images, err)
	}
}

func TestParseAPIProviderResources(t *testing.T) {
	tests := []struct {
		provider string
		data     string
		id       string
		ipv4     string
	}{
		{"contabo", `{"data":[{"instanceId":123,"ipConfig":{"v4":{"ip":"203.0.113.1"}}}]}`, "123", "203.0.113.1"},
		{"vultr", `{"instance":{"id":"vultr-1","plan":"vc2","main_ip":"203.0.113.2"}}`, "vultr-1", "203.0.113.2"},
		{"linode", `{"id":456,"region":"eu-west","ipv4":["192.0.2.2","203.0.113.3"]}`, "456", "192.0.2.2"},
		{"scaleway", `{"server":{"id":"scw-1","commercial_type":"DEV1-S","public_ip":{"address":"203.0.113.4"}}}`, "scw-1", "203.0.113.4"},
		{"scaleway", `{"id":"mac-1","type":"M4-S","ip":"203.0.113.8","ssh_username":"m4"}`, "mac-1", "203.0.113.8"},
		{"huawei-cloud", `{"server":{"id":"hw-1","OS-EXT-STS:vm_state":"active","addresses":{"net":[{"addr":"203.0.113.5","OS-EXT-IPS:type":"floating","version":4}]}}}`, "hw-1", "203.0.113.5"},
		{"ovhcloud", `{"id":"ovh-1","flavorId":"f1","ipAddresses":[{"ip":"203.0.113.6","type":"public","version":4}]}`, "ovh-1", "203.0.113.6"},
		{"aws-ec2", `{"reservationSet":{"item":{"instancesSet":{"item":{"instanceId":"i-123","ipAddress":"203.0.113.7"}}}}}`, "i-123", "203.0.113.7"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			id, ipv4, _ := parseProviderResource(tt.provider, json.RawMessage(tt.data))
			if id != tt.id || ipv4 != tt.ipv4 {
				t.Fatalf("resource = id %q ipv4 %q, want %q %q", id, ipv4, tt.id, tt.ipv4)
			}
		})
	}
}

func TestVultrCreateRequestEncodesCloudInit(t *testing.T) {
	_, args, err := apiProviderCreateRequest(nil, "vultr", CreateInstanceInput{Name: "test", Region: "ams", Size: "vc2", Image: "1743"}, "ssh-ed25519 AAAA test")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := args["user_data"].(string)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !strings.Contains(string(decoded), "ssh-ed25519 AAAA test") {
		t.Fatalf("user_data is not base64 cloud-init: %q, err=%v", encoded, err)
	}
}

func TestScalewaySSHKeyTagMatchesOfficialCLIEncoding(t *testing.T) {
	const publicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest apteva-instance"
	want := "AUTHORIZED_KEY=ssh-ed25519_AAAAC3NzaC1lZDI1NTE5AAAAITest_apteva-instance"
	if got := scalewaySSHKeyTag("  " + publicKey + "\n"); got != want {
		t.Fatalf("scalewaySSHKeyTag() = %q, want %q", got, want)
	}
}
