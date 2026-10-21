package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type catalogPrice struct {
	Known   bool    `json:"price_known"`
	Monthly float64 `json:"monthly_cost"`
	Hourly  float64 `json:"hourly_cost"`
}

func (p *catalogPrice) UnmarshalJSON(data []byte) error {
	var raw struct {
		Monthly *float64 `json:"monthly_cost"`
		Hourly  float64  `json:"hourly_cost"`
	}
	if e := json.Unmarshal(data, &raw); e != nil {
		return e
	}
	p.Known = raw.Monthly != nil
	p.Hourly = raw.Hourly
	if raw.Monthly != nil {
		p.Monthly = *raw.Monthly
	}
	return nil
}

type vultrPlan struct {
	ID              string                  `json:"id"`
	Type            string                  `json:"type"`
	VCPU            int                     `json:"vcpu_count"`
	LegacyVCPU      int                     `json:"vcpu"`
	CPUCount        int                     `json:"cpu_count"`
	CPUCores        int                     `json:"cpu_cores"`
	CPUModel        string                  `json:"cpu_model"`
	CPUManufacturer string                  `json:"cpu_manufacturer"`
	Architecture    string                  `json:"arch"`
	RAM             float64                 `json:"ram"`
	Disk            int                     `json:"disk"`
	DiskCount       int                     `json:"disk_count"`
	DiskType        string                  `json:"disk_type"`
	StorageType     string                  `json:"storage_type"`
	Monthly         *float64                `json:"monthly_cost"`
	Hourly          float64                 `json:"hourly_cost"`
	Locations       []string                `json:"locations"`
	OnDemand        *bool                   `json:"deploy_ondemand"`
	Preemptible     bool                    `json:"deploy_preemptible"`
	GPUBrand        string                  `json:"gpu_brand"`
	GPUType         string                  `json:"gpu_type"`
	GPUCount        int                     `json:"gpu_count"`
	GPUVRAM         float64                 `json:"gpu_vram_gb"`
	LocationCost    map[string]catalogPrice `json:"location_cost"`
}
type vultrMetadata struct {
	Kind         string `json:"kind"`
	Family       string `json:"family,omitempty"`
	LocalDisk    bool   `json:"local_disk,omitempty"`
	BootProtocol string `json:"boot_protocol,omitempty"`
	BootLabel    string `json:"boot_label,omitempty"`
}

func vultrMeta(raw string) vultrMetadata {
	var m vultrMetadata
	_ = json.Unmarshal([]byte(raw), &m)
	return m
}
func isVultrMetal(i *Instance) bool {
	if i == nil || i.Provider != "vultr" {
		return false
	}
	switch vultrMeta(i.ProviderMetadataJSON).Kind {
	case "bare_metal":
		return true
	case "instance":
		return false
	}
	if i.ResourceClass != "" {
		return i.ResourceClass == "bare_metal"
	}
	return strings.HasPrefix(i.Size, "vbm-") // Legacy rows only; metadata is authoritative.
}

func vultrServerTypes(ctx *sdk.AppCtx) ([]ServerType, error) {
	// No family filter: new compute/GPU families enter the generic catalog too.
	data, e := executeProviderTool(ctx, "vultr", "list_plans", map[string]any{"per_page": 500})
	if e != nil {
		return nil, e
	}
	types, e := parseVultrPlans(data, false, catalogScope(ctx).Zone)
	if e != nil {
		return nil, e
	}
	data, e = executeProviderTool(ctx, "vultr", "list_metal_plans", map[string]any{"per_page": 500})
	if e != nil {
		return nil, fmt.Errorf("Vultr bare-metal catalog unavailable; refresh the Server integration catalog to install list_metal_plans: %w", e)
	}
	metal, e := parseVultrPlans(data, true, catalogScope(ctx).Zone)
	if e != nil {
		return nil, e
	}
	seen := map[string]bool{}
	result := []ServerType{}
	for _, row := range append(types, metal...) {
		if seen[row.Name] {
			return nil, fmt.Errorf("ambiguous Vultr plan id %s", row.Name)
		}
		seen[row.Name] = true
		if kind := catalogScope(ctx).ResourceClass; kind != "" && kind != row.ResourceClass {
			continue
		}
		result = append(result, row)
	}
	return result, nil
}
func parseVultrPlans(data json.RawMessage, metal bool, region string) ([]ServerType, error) {
	var root map[string]json.RawMessage
	if e := json.Unmarshal(data, &root); e != nil {
		return nil, e
	}
	key := "plans"
	if metal {
		key = "plans_metal"
	}
	raw, ok := root[key]
	if !ok {
		return nil, fmt.Errorf("Vultr %s response missing", key)
	}
	var plans []vultrPlan
	if e := json.Unmarshal(raw, &plans); e != nil {
		return nil, e
	}
	rows := make([]ServerType, 0, len(plans))
	for _, p := range plans {
		if p.ID == "" {
			continue
		}
		cores := p.VCPU
		if cores == 0 {
			cores = p.LegacyVCPU
		}
		disk := p.Disk
		class := "virtual"
		family := p.Type
		cpu := vultrCPUType(p.Type)
		if strings.Contains(strings.ToUpper(p.DiskType), "DEDICATED") {
			cpu = "dedicated"
		}
		arch := normalizeArchitecture(p.Architecture)
		if p.Architecture == "" {
			arch = "x86"
		}
		if metal {
			class = "bare_metal"
			family = "bare_metal"
			cpu = "dedicated"
			cores = p.CPUCores
			if cores == 0 {
				cores = p.CPUCount
			}
			if p.DiskCount > 0 {
				disk *= p.DiskCount
			}
			if strings.Contains(strings.ToLower(p.CPUModel), "grace") || strings.Contains(strings.ToLower(p.CPUModel), "neoverse") {
				arch = "arm"
			}
		}
		available := len(p.Locations) > 0 && (p.OnDemand == nil || *p.OnDemand)
		reason := ""
		if len(p.Locations) == 0 {
			reason = "No available regions reported by Vultr"
		}
		if p.OnDemand != nil && !*p.OnDemand {
			reason = "On-demand deployment unavailable"
			if p.Preemptible {
				reason += "; provider advertises preemptible capacity only"
			}
		}
		known := p.Monthly != nil
		monthly := float64(0)
		if known {
			monthly = *p.Monthly
		} else {
			available = false
			reason = "Provider price unavailable"
		}
		if quote, ok := p.LocationCost[region]; ok {
			monthly = quote.Monthly
			p.Hourly = quote.Hourly
			if !quote.Known {
				known = false
				available = false
				reason = "Regional provider price unavailable"
			}
		}
		configurable := false
		row := ServerType{ConfigurableBoot: &configurable, Name: p.ID, Description: p.ID, Cores: cores, MemoryGB: p.RAM / 1024, DiskGB: disk, CPUType: cpu, Architecture: arch, Platform: "linux", ResourceClass: class, ProviderFamily: family, Available: &available, UnavailableReason: reason, PriceKnown: &known, MonthlyPriceUSD: monthly, HourlyPriceUSD: p.Hourly, AvailableIn: p.Locations, LocationPrices: p.LocationCost}
		if p.CPUModel != "" {
			row.Description += " — " + p.CPUModel
		}
		if p.GPUType != "" || p.GPUCount > 0 {
			memory := p.GPUVRAM
			if p.GPUCount > 0 {
				memory /= float64(p.GPUCount)
			}
			row.Accelerators = []AcceleratorDef{{Kind: "gpu", Vendor: p.GPUBrand, Model: p.GPUType, Count: p.GPUCount, MemoryGB: memory}}
		}
		local := disk > 0 && p.StorageType != "block_storage"
		if local {
			row.BootStorage = []StorageConstraint{{StorageClass: "local", ProviderType: "local", AllowedTiers: []string{"local"}, MinSizeGB: disk, MaxSizeGB: disk, Technology: vultrDiskTechnology(p), Billing: "included"}}
		}
		if !metal && (strings.Contains(p.StorageType, "block") || p.Type == "vx1" || strings.HasPrefix(p.ID, "vx1-")) {
			configurable = true
			row.BootStorage = append(row.BootStorage, StorageConstraint{StorageClass: "block", ProviderType: "high_perf", AllowedTiers: []string{"performance"}, MinSizeGB: 1, Persistent: true, Technology: "nvme", Billing: "separate"})
			row.RequiresBootStorage = !local
			if !local {
				row.DiskGB = 0
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}
func configureVultrPlan(in *CreateInstanceInput, p ServerType) error {
	if p.Available != nil && !*p.Available {
		return fmt.Errorf("Vultr plan %s unavailable: %s", p.Name, p.UnavailableReason)
	}
	if p.PriceKnown != nil && !*p.PriceKnown {
		return errors.New("Vultr provider price unavailable")
	}
	if p.MonthlyPriceUSD < 0 {
		return errors.New("invalid Vultr quote")
	}
	in.ResourceClass = p.ResourceClass
	in.Platform = p.Platform
	in.ResourcesJSON = nativeJSON(InstanceResources{CPU: &CPUResource{Cores: float64(p.Cores)}, MemoryGB: p.MemoryGB, DiskGB: p.DiskGB, Accelerators: p.Accelerators})
	meta := vultrMetadata{Kind: "instance", Family: p.ProviderFamily, LocalDisk: p.DiskGB > 0}
	if p.ConfigurableBoot != nil && *p.ConfigurableBoot {
		meta.BootProtocol = "block_devices"
	}
	if p.ResourceClass == "bare_metal" {
		meta.Kind = "bare_metal"
	}
	boot := in.Storage.Boot
	if p.RequiresBootStorage && boot == nil {
		return errors.New("this diskless Vultr plan requires explicit storage.boot with a block volume size; storage is billed separately")
	}
	if boot != nil {
		if p.ConfigurableBoot == nil || !*p.ConfigurableBoot {
			return errors.New("selected Vultr plan has fixed local boot storage; omit storage.boot")
		}
		if boot.StorageClass == "local" {
			if !meta.LocalDisk || boot.SizeGB != p.DiskGB {
				return errors.New("local Vultr boot storage must match the plan's fixed capacity")
			}
			if boot.DeletePolicy == "retain" {
				return errors.New("local boot storage cannot be retained")
			}
		} else {
			if boot.StorageClass != "block" || boot.SizeGB <= 0 {
				return errors.New("Vultr boot block storage requires a positive size")
			}
			if boot.ProviderType != "" && boot.ProviderType != "high_perf" || boot.Tier != "performance" && boot.Tier != "provider-default" && boot.Tier != "" {
				return errors.New("Vultr bootable block storage requires the performance/high_perf tier")
			}
			boot.Tier = "performance"
			boot.ProviderType = "high_perf"
			meta.BootLabel = "apteva-boot-" + newRequestID()
		}
	}
	in.ProviderMetadataJSON = nativeJSON(meta)
	return nil
}
func vultrCreateRequest(in CreateInstanceInput, pub string) (string, map[string]any, error) {
	osID, e := strconv.Atoi(in.Image)
	if e != nil {
		return "", nil, e
	}
	args := map[string]any{"region": in.Region, "plan": in.Size, "os_id": osID, "label": in.Name, "hostname": in.Name, "user_data": base64.StdEncoding.EncodeToString([]byte(buildCloudInit(pub))), "enable_ipv6": true, "user_scheme": "root"}
	meta := vultrMeta(in.ProviderMetadataJSON)
	if in.ResourceClass == "bare_metal" || meta.Kind == "bare_metal" {
		return "create_bare_metal", args, nil
	}
	blocks := []map[string]any{}
	if meta.BootProtocol == "block_devices" || meta.Family == "vx1" || strings.HasPrefix(in.Size, "vx1-") {
		if boot := in.Storage.Boot; boot != nil && boot.StorageClass == "block" {
			if meta.BootLabel == "" {
				return "", nil, errors.New("Vultr boot volume ownership label missing")
			}
			blocks = append(blocks, map[string]any{"disk_size": boot.SizeGB, "label": meta.BootLabel, "bootable": true})
			if meta.LocalDisk {
				blocks = append(blocks, map[string]any{"block_id": "local"})
			}
		} else {
			blocks = append(blocks, map[string]any{"block_id": "local", "bootable": true})
		}
	}
	args["block_devices"] = blocks
	return "create_instance", args, nil
}

func validateVultrAvailability(ctx *sdk.AppCtx, in *CreateInstanceInput) error {
	typ := "all"
	if in.ResourceClass == "bare_metal" {
		typ = "vbm"
	}
	data, e := executeProviderTool(ctx, "vultr", "list_region_availability", map[string]any{"region_id": in.Region, "type": typ})
	if e != nil {
		return fmt.Errorf("verify live Vultr region availability (refresh Server integration catalog if this operation is missing): %w", e)
	}
	var result struct {
		Plans []string `json:"available_plans"`
	}
	if e = json.Unmarshal(data, &result); e != nil {
		return e
	}
	if !containsString(result.Plans, in.Size) {
		return fmt.Errorf("Vultr plan %s has no current public on-demand capacity in %s", in.Size, in.Region)
	}
	images, e := apiProviderListImages(ctx, "vultr")
	if e != nil {
		return e
	}
	for _, image := range images {
		if image.Name == in.Image {
			return nil
		}
	}
	return fmt.Errorf("Vultr OS %s is not a supported catalog OS image", in.Image)
}

func vultrDiskTechnology(p vultrPlan) string {
	text := strings.ToLower(p.Type + " " + p.DiskType)
	if strings.Contains(text, "nvme") || p.Type == "vx1" {
		return "nvme"
	}
	if strings.Contains(text, "ssd") {
		return "ssd"
	}
	return ""
}
