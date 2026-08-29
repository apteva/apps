package main

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

func parseProviderServerTypes(provider string, data json.RawMessage) ([]ServerType, error) {
	switch provider {
	case "vultr":
		var v struct {
			Plans []struct {
				ID          string   `json:"id"`
				VCPU        int      `json:"vcpu_count"`
				VCPULegacy  int      `json:"vcpu"`
				RAM         int      `json:"ram"`
				Disk        int      `json:"disk"`
				Type        string   `json:"type"`
				MonthlyCost float64  `json:"monthly_cost"`
				HourlyCost  float64  `json:"hourly_cost"`
				Locations   []string `json:"locations"`
			} `json:"plans"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("decode vultr plans: %w", err)
		}
		out := make([]ServerType, 0, len(v.Plans))
		for _, p := range v.Plans {
			cores := p.VCPU
			if cores == 0 {
				cores = p.VCPULegacy
			}
			if p.ID == "" {
				continue
			}
			out = append(out, ServerType{Name: p.ID, Description: p.ID, Cores: cores, MemoryGB: float64(p.RAM) / 1024, DiskGB: p.Disk, CPUType: vultrCPUType(p.Type), Architecture: "x86", MonthlyPriceUSD: p.MonthlyCost, HourlyPriceUSD: p.HourlyCost, AvailableIn: p.Locations})
		}
		return out, nil
	case "linode":
		var v struct {
			Data []struct {
				ID     string `json:"id"`
				Label  string `json:"label"`
				Class  string `json:"class"`
				VCPUs  int    `json:"vcpus"`
				Memory int    `json:"memory"`
				Disk   int    `json:"disk"`
				Price  struct {
					Hourly  float64 `json:"hourly"`
					Monthly float64 `json:"monthly"`
				} `json:"price"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("decode linode types: %w", err)
		}
		out := make([]ServerType, 0, len(v.Data))
		for _, p := range v.Data {
			if p.ID == "" {
				continue
			}
			out = append(out, ServerType{Name: p.ID, Description: p.Label, Cores: p.VCPUs, MemoryGB: float64(p.Memory) / 1024, DiskGB: p.Disk / 1024, CPUType: linodeCPUType(p.Class), Architecture: "x86", MonthlyPriceUSD: p.Price.Monthly, HourlyPriceUSD: p.Price.Hourly})
		}
		return out, nil
	case "scaleway":
		var v struct {
			Servers map[string]struct {
				NCPUs             int             `json:"ncpus"`
				RAM               int64           `json:"ram"`
				Arch              string          `json:"arch"`
				HourlyPrice       json.RawMessage `json:"hourly_price"`
				MonthlyPrice      json.RawMessage `json:"monthly_price"`
				VolumesConstraint struct {
					MinSize int64 `json:"min_size"`
					MaxSize int64 `json:"max_size"`
				} `json:"volumes_constraint"`
				PerVolumeConstraint map[string]struct {
					MinSize int64 `json:"min_size"`
					MaxSize int64 `json:"max_size"`
				} `json:"per_volume_constraint"`
				Capabilities struct {
					BlockStorage bool `json:"block_storage"`
				} `json:"capabilities"`
			} `json:"servers"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("decode scaleway server products: %w", err)
		}
		out := make([]ServerType, 0, len(v.Servers))
		for name, p := range v.Servers {
			bootStorage := make([]StorageConstraint, 0, 2)
			if local, ok := p.PerVolumeConstraint["l_ssd"]; ok {
				maxSize := minNonZeroInt64(p.VolumesConstraint.MaxSize, local.MaxSize)
				if maxSize > 0 {
					bootStorage = append(bootStorage, StorageConstraint{
						StorageClass: "local", ProviderType: "l_ssd",
						MinSizeGB: decimalGBInt(maxInt64(p.VolumesConstraint.MinSize, local.MinSize)),
						MaxSizeGB: decimalGBInt(maxSize),
					})
				}
			}
			if p.Capabilities.BlockStorage {
				bootStorage = append(bootStorage, StorageConstraint{StorageClass: "block", ProviderType: "sbs_volume"})
			}
			diskGB := 0
			for _, constraint := range bootStorage {
				if constraint.StorageClass == "local" {
					diskGB = constraint.MaxSizeGB
					break
				}
			}
			out = append(out, ServerType{Name: name, Description: name, Cores: p.NCPUs, MemoryGB: bytesToGB(p.RAM), DiskGB: diskGB, CPUType: "shared", Architecture: normalizeArchitecture(p.Arch), MonthlyPriceEUR: flexiblePrice(p.MonthlyPrice), HourlyPriceEUR: flexiblePrice(p.HourlyPrice), BootStorage: bootStorage})
		}
		return out, nil
	case "huawei-cloud":
		var v struct {
			Flavors []struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				VCPUs any    `json:"vcpus"`
				RAM   int    `json:"ram"`
				Disk  int    `json:"disk"`
			} `json:"flavors"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("decode huawei flavors: %w", err)
		}
		out := make([]ServerType, 0, len(v.Flavors))
		for _, p := range v.Flavors {
			if p.ID == "" {
				continue
			}
			out = append(out, ServerType{Name: p.ID, Description: firstNonEmpty(p.Name, p.ID), Cores: anyToInt(p.VCPUs), MemoryGB: float64(p.RAM) / 1024, DiskGB: p.Disk, CPUType: "shared", Architecture: "x86"})
		}
		return out, nil
	case "ovhcloud":
		var rows []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Region       string `json:"region"`
			VCPUs        int    `json:"vcpus"`
			RAM          int    `json:"ram"`
			Disk         int    `json:"disk"`
			Available    *bool  `json:"available"`
			MonthlyPrice struct {
				Value float64 `json:"value"`
			} `json:"monthlyPrice"`
			HourlyPrice struct {
				Value float64 `json:"value"`
			} `json:"hourlyPrice"`
		}
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, fmt.Errorf("decode ovhcloud flavors: %w", err)
		}
		out := make([]ServerType, 0, len(rows))
		for _, p := range rows {
			if p.ID == "" || (p.Available != nil && !*p.Available) {
				continue
			}
			available := []string{}
			if p.Region != "" {
				available = append(available, p.Region)
			}
			out = append(out, ServerType{Name: p.ID, Description: p.Name, Cores: p.VCPUs, MemoryGB: float64(p.RAM) / 1024, DiskGB: p.Disk, CPUType: "shared", Architecture: "x86", MonthlyPriceUSD: p.MonthlyPrice.Value, HourlyPriceUSD: p.HourlyPrice.Value, AvailableIn: available})
		}
		return out, nil
	case "aws-ec2":
		root, err := decodeJSONObject(data)
		if err != nil {
			return nil, fmt.Errorf("decode aws instance types: %w", err)
		}
		items := collectObjectsForKey(root, "item")
		out := make([]ServerType, 0, len(items))
		for _, item := range items {
			name := mapString(item, "instanceType")
			if name == "" || mapValue(item, "vCpuInfo") == nil {
				continue
			}
			cores := nestedInt(item, "vCpuInfo", "defaultVCpus")
			memory := nestedInt(item, "memoryInfo", "sizeInMiB")
			disk := nestedInt(item, "instanceStorageInfo", "totalSizeInGB")
			arch := firstNestedScalar(item, "processorInfo", "supportedArchitectures", "item")
			out = append(out, ServerType{Name: name, Description: name, Cores: cores, MemoryGB: float64(memory) / 1024, DiskGB: disk, CPUType: awsCPUType(name), Architecture: normalizeArchitecture(arch)})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("no server type parser for provider %q", provider)
	}
}

func parseProviderLocations(provider string, data json.RawMessage) ([]Location, error) {
	switch provider {
	case "vultr":
		var v struct {
			Regions []struct {
				ID        string `json:"id"`
				City      string `json:"city"`
				Country   string `json:"country"`
				Continent string `json:"continent"`
			} `json:"regions"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		out := make([]Location, 0, len(v.Regions))
		for _, r := range v.Regions {
			if r.ID != "" {
				out = append(out, Location{Name: r.ID, City: r.City, Country: r.Country, NetworkZone: strings.ToLower(r.Continent), Description: strings.TrimSpace(r.City + ", " + r.Country)})
			}
		}
		return out, nil
	case "linode":
		var v struct {
			Data []struct {
				ID      string `json:"id"`
				Country string `json:"country"`
				Status  string `json:"status"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		out := make([]Location, 0, len(v.Data))
		for _, r := range v.Data {
			if r.ID != "" && (r.Status == "" || r.Status == "ok") {
				out = append(out, Location{Name: r.ID, Country: r.Country, Description: r.ID})
			}
		}
		return out, nil
	case "huawei-cloud":
		var v struct {
			AvailabilityZoneInfo []struct {
				ZoneName  string `json:"zoneName"`
				ZoneState struct {
					Available bool `json:"available"`
				} `json:"zoneState"`
			} `json:"availabilityZoneInfo"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		out := make([]Location, 0, len(v.AvailabilityZoneInfo))
		for _, z := range v.AvailabilityZoneInfo {
			if z.ZoneName != "" && z.ZoneState.Available {
				out = append(out, Location{Name: z.ZoneName, Description: z.ZoneName})
			}
		}
		return out, nil
	case "ovhcloud":
		var names []string
		if err := json.Unmarshal(data, &names); err == nil {
			out := make([]Location, 0, len(names))
			for _, name := range names {
				if name != "" {
					out = append(out, Location{Name: name, Description: name})
				}
			}
			return out, nil
		}
		var rows []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, err
		}
		out := make([]Location, 0, len(rows))
		for _, r := range rows {
			if r.Name != "" {
				out = append(out, Location{Name: r.Name, Description: r.Name})
			}
		}
		return out, nil
	case "aws-ec2":
		root, err := decodeJSONObject(data)
		if err != nil {
			return nil, err
		}
		items := collectObjectsForKey(root, "item")
		out := make([]Location, 0, len(items))
		for _, item := range items {
			name := mapString(item, "zoneName")
			state := mapString(item, "zoneState")
			if name != "" && (state == "" || state == "available") {
				out = append(out, Location{Name: name, NetworkZone: mapString(item, "regionName"), Description: name})
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("no location parser for provider %q", provider)
	}
}

func parseProviderImages(provider string, data json.RawMessage) ([]Image, error) {
	switch provider {
	case "contabo":
		var v struct {
			Data []struct {
				ID            string `json:"imageId"`
				Name          string `json:"name"`
				Description   string `json:"description"`
				OS            string `json:"osType"`
				Version       string `json:"version"`
				StandardImage *bool  `json:"standardImage"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		out := make([]Image, 0, len(v.Data))
		for _, im := range v.Data {
			if im.ID != "" && (im.StandardImage == nil || *im.StandardImage) {
				out = append(out, Image{Name: im.ID, Description: firstNonEmpty(im.Description, im.Name), OSFlavor: strings.ToLower(im.OS), OSVersion: im.Version})
			}
		}
		return out, nil
	case "vultr":
		var v struct {
			OS []struct {
				ID     any    `json:"id"`
				Name   string `json:"name"`
				Arch   string `json:"arch"`
				Family string `json:"family"`
			} `json:"os"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		out := make([]Image, 0, len(v.OS))
		for _, im := range v.OS {
			id := anyToString(im.ID)
			if id != "" && strings.ToLower(im.Family) != "windows" {
				out = append(out, Image{Name: id, Description: im.Name, OSFlavor: strings.ToLower(im.Family), OSVersion: imageVersion(im.Name), Architecture: normalizeArchitecture(im.Arch)})
			}
		}
		return out, nil
	case "linode":
		var v struct {
			Data []struct {
				ID          string `json:"id"`
				Label       string `json:"label"`
				Description string `json:"description"`
				Vendor      string `json:"vendor"`
				Deprecated  bool   `json:"deprecated"`
				Public      bool   `json:"is_public"`
				Size        int    `json:"size"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		out := make([]Image, 0, len(v.Data))
		for _, im := range v.Data {
			if im.ID != "" && im.Public && !im.Deprecated {
				out = append(out, Image{Name: im.ID, Description: firstNonEmpty(im.Label, im.Description), OSFlavor: strings.ToLower(im.Vendor), OSVersion: imageVersion(im.Label), Architecture: "x86", DiskSizeGB: im.Size / 1024})
			}
		}
		return out, nil
	case "scaleway":
		var v struct {
			Images []struct {
				ID         string `json:"id"`
				Name       string `json:"name"`
				Arch       string `json:"arch"`
				Public     bool   `json:"public"`
				State      string `json:"state"`
				RootVolume struct {
					Size       int64  `json:"size"`
					VolumeType string `json:"volume_type"`
				} `json:"root_volume"`
			} `json:"images"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		out := make([]Image, 0, len(v.Images))
		for _, im := range v.Images {
			if im.ID != "" && im.Public && (im.State == "" || im.State == "available") {
				out = append(out, Image{Name: im.ID, Description: im.Name, OSFlavor: imageFlavor(im.Name), OSVersion: imageVersion(im.Name), Architecture: normalizeArchitecture(im.Arch), DiskSizeGB: decimalGBInt(im.RootVolume.Size), ProviderType: scalewayImageProviderType(im.RootVolume.VolumeType)})
			}
		}
		return out, nil
	case "huawei-cloud":
		var v struct {
			Images []struct {
				ID         string `json:"id"`
				Name       string `json:"name"`
				OS         string `json:"__os_type"`
				Version    string `json:"__os_version"`
				Status     string `json:"status"`
				MinDisk    int    `json:"min_disk"`
				VirtualEnv string `json:"virtual_env_type"`
			} `json:"images"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		out := make([]Image, 0, len(v.Images))
		for _, im := range v.Images {
			if im.ID != "" && strings.EqualFold(im.OS, "linux") && (im.Status == "" || strings.EqualFold(im.Status, "active")) {
				out = append(out, Image{Name: im.ID, Description: im.Name, OSFlavor: imageFlavor(im.Name), OSVersion: firstNonEmpty(im.Version, imageVersion(im.Name)), Architecture: normalizeArchitecture(im.VirtualEnv), DiskSizeGB: im.MinDisk})
			}
		}
		return out, nil
	case "ovhcloud":
		var rows []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Type       string `json:"type"`
			Region     string `json:"region"`
			Status     string `json:"status"`
			MinDisk    int    `json:"minDisk"`
			Visibility string `json:"visibility"`
		}
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, err
		}
		out := make([]Image, 0, len(rows))
		for _, im := range rows {
			if im.ID != "" && !strings.Contains(strings.ToLower(im.Type), "windows") && (im.Status == "" || strings.EqualFold(im.Status, "active")) {
				out = append(out, Image{Name: im.ID, Description: im.Name, OSFlavor: imageFlavor(im.Name), OSVersion: imageVersion(im.Name), Architecture: "x86", DiskSizeGB: im.MinDisk})
			}
		}
		return out, nil
	case "aws-ec2":
		root, err := decodeJSONObject(data)
		if err != nil {
			return nil, err
		}
		items := collectObjectsForKey(root, "item")
		out := make([]Image, 0, len(items))
		for _, item := range items {
			id := mapString(item, "imageId")
			if id == "" || mapString(item, "state") != "available" {
				continue
			}
			name := mapString(item, "name")
			out = append(out, Image{Name: id, Description: firstNonEmpty(name, mapString(item, "description")), OSFlavor: imageFlavor(name), OSVersion: imageVersion(name), Architecture: normalizeArchitecture(mapString(item, "architecture"))})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("no image parser for provider %q", provider)
	}
}

func parseProviderResource(provider string, data json.RawMessage) (id, ipv4, ipv6 string) {
	var root any
	if json.Unmarshal(data, &root) != nil {
		return "", "", ""
	}
	objects := collectMaps(root)
	for _, obj := range objects {
		candidateID := ""
		switch provider {
		case "contabo":
			candidateID = firstMapString(obj, "instanceId")
			if candidateID != "" {
				if cfg, ok := mapValue(obj, "ipConfig").(map[string]any); ok {
					ipv4 = nestedMapString(cfg, "v4", "ip")
					ipv6 = nestedMapString(cfg, "v6", "ip")
				}
			}
		case "vultr":
			candidateID = mapString(obj, "id")
			if candidateID != "" && (mapValue(obj, "main_ip") != nil || mapValue(obj, "default_password") != nil || mapValue(obj, "plan") != nil) {
				ipv4, ipv6 = mapString(obj, "main_ip"), mapString(obj, "v6_main_ip")
			}
		case "linode":
			candidateID = mapString(obj, "id")
			if candidateID != "" && mapValue(obj, "region") != nil {
				ipv4 = firstPublicIP(mapStringSlice(obj, "ipv4"), 4)
				ipv6 = firstIPFromCIDR(mapString(obj, "ipv6"), 6)
			}
		case "scaleway":
			candidateID = mapString(obj, "id")
			if candidateID != "" && (mapValue(obj, "commercial_type") != nil || mapValue(obj, "public_ip") != nil || mapValue(obj, "ssh_username") != nil || mapValue(obj, "interfaces") != nil) {
				ipv4 = nestedMapString(obj, "public_ip", "address")
				if ipv4 == "" {
					ipv4 = firstAddress(obj, "public_ips", 4)
				}
				if ipv4 == "" {
					ipv4 = firstAddress(obj, "interfaces", 4)
				}
				if ipv4 == "" {
					ipv4 = mapString(obj, "ip")
				}
				ipv6 = firstNonEmpty(nestedMapString(obj, "ipv6", "address"), firstAddress(obj, "public_ips", 6), firstAddress(obj, "interfaces", 6))
			}
		case "huawei-cloud":
			candidateID = firstMapString(obj, "id", "server_id")
			if candidateID != "" && (mapValue(obj, "addresses") != nil || mapValue(obj, "OS-EXT-STS:vm_state") != nil) {
				ipv4, ipv6 = huaweiAddresses(obj)
			}
		case "ovhcloud":
			candidateID = mapString(obj, "id")
			if candidateID != "" && (mapValue(obj, "ipAddresses") != nil || mapValue(obj, "flavorId") != nil) {
				ipv4, ipv6 = ovhAddresses(obj)
			}
		case "aws-ec2":
			candidateID = mapString(obj, "instanceId")
			if candidateID != "" {
				ipv4 = firstNonEmpty(mapString(obj, "ipAddress"), mapString(obj, "publicIpAddress"))
				ipv6 = findJSONScalarIn(obj, "ipv6Address")
			}
		}
		if candidateID != "" {
			return candidateID, ipv4, ipv6
		}
	}
	return "", "", ""
}

func contaboServerTypes() []ServerType {
	return []ServerType{
		{Name: "V153", Description: "Cloud VPS 4 (API catalog)", DiskGB: 100, CPUType: "shared", Architecture: "x86"},
		{Name: "V154", Description: "Cloud VPS 6 (API catalog)", DiskGB: 200, CPUType: "shared", Architecture: "x86"},
		{Name: "V155", Description: "Cloud VPS 8 (API catalog)", DiskGB: 300, CPUType: "shared", Architecture: "x86"},
		{Name: "V156", Description: "Cloud VPS 12 (API catalog)", DiskGB: 400, CPUType: "shared", Architecture: "x86"},
		{Name: "V157", Description: "Cloud VPS 16 (API catalog)", DiskGB: 500, CPUType: "shared", Architecture: "x86"},
		{Name: "V158", Description: "Cloud VPS 18 (API catalog)", DiskGB: 600, CPUType: "shared", Architecture: "x86"},
		{Name: "V159", Description: "Cloud VPS Plus 4 NVMe (API catalog)", DiskGB: 150, CPUType: "shared", Architecture: "x86"},
		{Name: "V160", Description: "Cloud VPS Plus 6 NVMe (API catalog)", DiskGB: 300, CPUType: "shared", Architecture: "x86"},
		{Name: "V161", Description: "Cloud VPS Plus 8 NVMe (API catalog)", DiskGB: 450, CPUType: "shared", Architecture: "x86"},
		{Name: "V162", Description: "Cloud VPS Plus 12 NVMe (API catalog)", DiskGB: 600, CPUType: "shared", Architecture: "x86"},
		{Name: "V163", Description: "Cloud VPS Plus 16 NVMe (API catalog)", DiskGB: 750, CPUType: "shared", Architecture: "x86"},
		{Name: "V164", Description: "Cloud VPS Plus 18 NVMe (API catalog)", DiskGB: 900, CPUType: "shared", Architecture: "x86"},
	}
}

func contaboLocations() []Location {
	return []Location{
		{Name: "EU", Description: "European Union", NetworkZone: "eu"}, {Name: "UK", Description: "United Kingdom", Country: "GB", NetworkZone: "eu"},
		{Name: "US-central", Description: "United States Central", Country: "US", NetworkZone: "us"}, {Name: "US-east", Description: "United States East", Country: "US", NetworkZone: "us"},
		{Name: "US-west", Description: "United States West", Country: "US", NetworkZone: "us"}, {Name: "SIN", Description: "Singapore", Country: "SG", NetworkZone: "ap"},
		{Name: "AUS", Description: "Australia", Country: "AU", NetworkZone: "ap"}, {Name: "JPN", Description: "Japan", Country: "JP", NetworkZone: "ap"}, {Name: "IND", Description: "India", Country: "IN", NetworkZone: "ap"},
	}
}

func scalewayLocations() []Location {
	return []Location{
		{Name: "fr-par-1", City: "Paris", Country: "FR", NetworkZone: "fr-par"}, {Name: "fr-par-2", City: "Paris", Country: "FR", NetworkZone: "fr-par"}, {Name: "fr-par-3", City: "Paris", Country: "FR", NetworkZone: "fr-par"},
		{Name: "nl-ams-1", City: "Amsterdam", Country: "NL", NetworkZone: "nl-ams"}, {Name: "nl-ams-2", City: "Amsterdam", Country: "NL", NetworkZone: "nl-ams"}, {Name: "nl-ams-3", City: "Amsterdam", Country: "NL", NetworkZone: "nl-ams"},
		{Name: "pl-waw-1", City: "Warsaw", Country: "PL", NetworkZone: "pl-waw"}, {Name: "pl-waw-2", City: "Warsaw", Country: "PL", NetworkZone: "pl-waw"}, {Name: "pl-waw-3", City: "Warsaw", Country: "PL", NetworkZone: "pl-waw"},
		{Name: "it-mil-1", City: "Milan", Country: "IT", NetworkZone: "it-mil"},
	}
}

func flexiblePrice(raw json.RawMessage) float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var number float64
	if json.Unmarshal(raw, &number) == nil {
		return number
	}
	var money struct {
		Value    float64 `json:"value"`
		Units    int64   `json:"units"`
		Nanos    int64   `json:"nanos"`
		Currency string  `json:"currency_code"`
	}
	if json.Unmarshal(raw, &money) == nil {
		if money.Value != 0 {
			return money.Value
		}
		return float64(money.Units) + float64(money.Nanos)/1e9
	}
	return 0
}

func decodeJSONObject(data json.RawMessage) (map[string]any, error) {
	var root map[string]any
	err := json.Unmarshal(data, &root)
	return root, err
}

func collectMaps(value any) []map[string]any {
	var out []map[string]any
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			out = append(out, x)
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(value)
	return out
}

func collectObjectsForKey(root map[string]any, key string) []map[string]any {
	var out []map[string]any
	for _, obj := range collectMaps(root) {
		value, ok := obj[key]
		if !ok {
			continue
		}
		switch x := value.(type) {
		case map[string]any:
			out = append(out, x)
		case []any:
			for _, item := range x {
				if m, ok := item.(map[string]any); ok {
					out = append(out, m)
				}
			}
		}
	}
	return out
}

func mapValue(m map[string]any, key string) any {
	if v, ok := m[key]; ok {
		return v
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return nil
}

func mapString(m map[string]any, key string) string { return scalarString(mapValue(m, key)) }

func firstMapString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := mapString(m, key); v != "" {
			return v
		}
	}
	return ""
}

func nestedMapString(m map[string]any, path ...string) string {
	var value any = m
	for _, key := range path {
		obj, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		value = mapValue(obj, key)
	}
	return scalarString(value)
}

func nestedInt(m map[string]any, path ...string) int {
	return anyToInt(nestedValue(m, path...))
}

func nestedValue(m map[string]any, path ...string) any {
	var value any = m
	for _, key := range path {
		obj, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = mapValue(obj, key)
	}
	return value
}

func firstNestedScalar(m map[string]any, path ...string) string {
	v := nestedValue(m, path...)
	if values, ok := v.([]any); ok && len(values) > 0 {
		return scalarString(values[0])
	}
	return scalarString(v)
}

func scalarString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	default:
		return ""
	}
}

func anyToInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	default:
		return 0
	}
}

func jsonStringAt(data json.RawMessage, key string) string {
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return ""
	}
	return mapString(root, key)
}

func findJSONScalar(data json.RawMessage, key string) string {
	var root any
	if json.Unmarshal(data, &root) != nil {
		return ""
	}
	return findJSONScalarIn(root, key)
}

func findJSONScalarIn(root any, key string) string {
	for _, obj := range collectMaps(root) {
		if value := mapString(obj, key); value != "" {
			return value
		}
	}
	return ""
}

func firstJSONScalar(data json.RawMessage, listKey, scalarKey string) string {
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return ""
	}
	list := mapValue(root, listKey)
	for _, obj := range collectMaps(list) {
		if value := mapString(obj, scalarKey); value != "" {
			return value
		}
	}
	return ""
}

func mapStringSlice(m map[string]any, key string) []string {
	value := mapValue(m, key)
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s := scalarString(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstAddress(m map[string]any, key string, version int) string {
	for _, obj := range collectMaps(mapValue(m, key)) {
		address := firstMapString(obj, "address", "ip", "addr")
		if isIPVersion(address, version) {
			return address
		}
	}
	return ""
}

func firstPublicIP(values []string, version int) string {
	for _, value := range values {
		ip := net.ParseIP(strings.Split(value, "/")[0])
		if ip == nil || !isIPVersion(value, version) || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		return strings.Split(value, "/")[0]
	}
	return ""
}

func firstIPFromCIDR(value string, version int) string {
	ip := strings.Split(value, "/")[0]
	if isIPVersion(ip, version) {
		return ip
	}
	return ""
}

func isIPVersion(value string, version int) bool {
	ip := net.ParseIP(strings.Split(value, "/")[0])
	if ip == nil {
		return false
	}
	if version == 4 {
		return ip.To4() != nil
	}
	return ip.To4() == nil
}

func huaweiAddresses(m map[string]any) (string, string) {
	addresses, ok := mapValue(m, "addresses").(map[string]any)
	if !ok {
		return "", ""
	}
	var ipv4, ipv6 string
	for _, network := range addresses {
		for _, obj := range collectMaps(network) {
			address := firstMapString(obj, "addr", "address")
			typeName := strings.ToLower(firstMapString(obj, "OS-EXT-IPS:type", "type"))
			if typeName != "" && typeName != "floating" {
				continue
			}
			if ipv4 == "" && isIPVersion(address, 4) {
				ipv4 = address
			}
			if ipv6 == "" && isIPVersion(address, 6) {
				ipv6 = address
			}
		}
	}
	return ipv4, ipv6
}

func ovhAddresses(m map[string]any) (string, string) {
	var ipv4, ipv6 string
	for _, obj := range collectMaps(mapValue(m, "ipAddresses")) {
		address := firstMapString(obj, "ip", "address")
		typeName := strings.ToLower(mapString(obj, "type"))
		if typeName != "" && typeName != "public" {
			continue
		}
		if ipv4 == "" && isIPVersion(address, 4) {
			ipv4 = address
		}
		if ipv6 == "" && isIPVersion(address, 6) {
			ipv6 = address
		}
	}
	return ipv4, ipv6
}

func bytesToGB(bytes int64) float64 {
	if bytes <= 0 {
		return 0
	}
	return float64(bytes) / (1024 * 1024 * 1024)
}

func decimalGBInt(bytes int64) int {
	if bytes <= 0 {
		return 0
	}
	return int(bytes / 1_000_000_000)
}

func minNonZeroInt64(a, b int64) int64 {
	if a <= 0 {
		return b
	}
	if b <= 0 || a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func scalewayImageProviderType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "l_ssd", "l_ssd_snapshot", "local", "instance_local":
		return "l_ssd"
	case "sbs", "sbs_volume", "sbs_snapshot", "b_ssd", "instance_sbs":
		return "sbs_volume"
	default:
		return ""
	}
}

func normalizeArchitecture(value string) string {
	v := strings.ToLower(value)
	if strings.Contains(v, "arm") || strings.Contains(v, "aarch") {
		return "arm"
	}
	if v != "" {
		return "x86"
	}
	return ""
}

func imageFlavor(value string) string {
	v := strings.ToLower(value)
	for _, flavor := range []string{"ubuntu", "debian", "fedora", "centos", "rocky", "almalinux", "arch"} {
		if strings.Contains(v, flavor) {
			return flavor
		}
	}
	return "linux"
}

func imageVersion(value string) string {
	fields := strings.FieldsFunc(value, func(r rune) bool { return !(r >= '0' && r <= '9') && r != '.' })
	for _, field := range fields {
		if strings.Contains(field, ".") || len(field) == 2 || len(field) == 4 {
			return field
		}
	}
	return ""
}

func vultrCPUType(value string) string {
	if strings.Contains(strings.ToLower(value), "dedicated") || strings.Contains(strings.ToLower(value), "vhf") {
		return "dedicated"
	}
	return "shared"
}

func linodeCPUType(value string) string {
	if strings.Contains(strings.ToLower(value), "dedicated") {
		return "dedicated"
	}
	return "shared"
}

func awsCPUType(name string) string {
	if strings.HasPrefix(name, "t") {
		return "shared"
	}
	return "dedicated"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
