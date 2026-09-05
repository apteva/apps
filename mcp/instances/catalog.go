package main

// Live catalog queries — server types, locations, OS images — pulled
// from the bound VPS provider in real time rather than hardcoded in
// the manifest, the panel, or the user's head. Hetzner ages out
// server types (cpx* deprecated mid-2025), adds new locations
// (Singapore, US-East), and rolls forward OS images monthly. Anything
// we baked into a select-options list would be stale within months.
//
// Shape is provider-agnostic: the returned rows carry name + display
// + capacity + price fields that every IaaS exposes in some form.
// Hetzner's own shape lands as the first implemented adapter; future
// provider adapters normalize into this same envelope.
//
// No caching for now — the panel only calls these on dialog-open.
// If/when a worker starts hammering them we can add a 5-min
// in-memory cache without changing the call sites.

import (
	"encoding/json"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

// ─── output shapes ─────────────────────────────────────────────────

// ServerType is the per-call shape returned to MCP + HTTP callers.
// Fields that don't apply to a given provider stay zero.
type ServerType struct {
	Name            string           `json:"name"`
	Description     string           `json:"description,omitempty"`
	Cores           int              `json:"cores"`
	MemoryGB        float64          `json:"memory_gb"`
	DiskGB          int              `json:"disk_gb"`
	CPUType         string           `json:"cpu_type,omitempty"`       // shared | dedicated
	Architecture    string           `json:"architecture,omitempty"`   // x86 | arm
	Platform        string           `json:"platform,omitempty"`       // linux | macos | windows
	ResourceClass   string           `json:"resource_class,omitempty"` // virtual | bare_metal | container
	Accelerators    []AcceleratorDef `json:"accelerators,omitempty"`
	Deprecated      bool             `json:"deprecated,omitempty"`
	MonthlyPriceEUR float64          `json:"monthly_price_eur,omitempty"`
	HourlyPriceEUR  float64          `json:"hourly_price_eur,omitempty"`
	MonthlyPriceUSD float64          `json:"monthly_price_usd,omitempty"`
	HourlyPriceUSD  float64          `json:"hourly_price_usd,omitempty"`
	// AvailableIn lists location names where this type can be
	// provisioned. Hetzner ships some types only in newer regions.
	AvailableIn        []string            `json:"available_in,omitempty"`
	BootStorage        []StorageConstraint `json:"boot_storage,omitempty"`
	IncompatibleImages []string            `json:"incompatible_images,omitempty"`
}

// StorageConstraint describes one boot-storage option supported by a
// particular server type. Provider-wide capabilities are not sufficient for
// clouds such as Scaleway, where DEV1 supports local SSD but POP2 is
// block-only.
type StorageConstraint struct {
	StorageClass  string `json:"storage_class"`
	ProviderType  string `json:"provider_type,omitempty"`
	MinSizeGB     int    `json:"min_size_gb,omitempty"`
	MaxSizeGB     int    `json:"max_size_gb,omitempty"`
	Technology    string `json:"technology,omitempty"`
	IOPS          int    `json:"iops,omitempty"`
	BandwidthMbps int    `json:"bandwidth_mbps,omitempty"`
	Persistent    bool   `json:"persistent"`
	Replication   string `json:"replication,omitempty"`
	Billing       string `json:"billing,omitempty"`
}

// Location is one VPS region.
type Location struct {
	Name        string  `json:"name"`
	City        string  `json:"city,omitempty"`
	Country     string  `json:"country,omitempty"`
	Description string  `json:"description,omitempty"`
	NetworkZone string  `json:"network_zone,omitempty"`
	Latitude    float64 `json:"latitude,omitempty"`
	Longitude   float64 `json:"longitude,omitempty"`
}

// Image is one bootable OS image.
type Image struct {
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	OSFlavor        string   `json:"os_flavor,omitempty"`  // ubuntu | debian | …
	OSVersion       string   `json:"os_version,omitempty"` // 24.04 | 12 | …
	Architecture    string   `json:"architecture,omitempty"`
	DiskSizeGB      int      `json:"disk_size_gb,omitempty"`
	Platform        string   `json:"platform,omitempty"`
	ResourceClass   string   `json:"resource_class,omitempty"`
	AvailableIn     []string `json:"available_in,omitempty"`
	CompatibleTypes []string `json:"compatible_types,omitempty"`
	ProviderType    string   `json:"provider_type,omitempty"`
}

// ─── entry points ──────────────────────────────────────────────────

func listServerTypes(ctx *sdk.AppCtx, provider string) ([]ServerType, error) {
	resolved, err := resolveInstanceProvider(ctx, provider)
	if err != nil {
		return nil, err
	}
	switch resolved {
	case "hetzner":
		return hetznerListServerTypes(ctx)
	case "digitalocean":
		return digitalOceanListServerTypes(ctx)
	case "runpod":
		return runPodListServerTypes(ctx)
	default:
		if isAPIProvider(resolved) {
			return apiProviderListServerTypes(ctx, resolved)
		}
		return nil, providerAdapterUnavailable(resolved, "server type catalog")
	}
}

func listLocations(ctx *sdk.AppCtx, provider string) ([]Location, error) {
	resolved, err := resolveInstanceProvider(ctx, provider)
	if err != nil {
		return nil, err
	}
	switch resolved {
	case "hetzner":
		return hetznerListLocations(ctx)
	case "digitalocean":
		return digitalOceanListLocations(ctx)
	case "runpod":
		return runPodListLocations(ctx)
	default:
		if isAPIProvider(resolved) {
			return apiProviderListLocations(ctx, resolved)
		}
		return nil, providerAdapterUnavailable(resolved, "location catalog")
	}
}

func listImages(ctx *sdk.AppCtx, provider string) ([]Image, error) {
	resolved, err := resolveInstanceProvider(ctx, provider)
	if err != nil {
		return nil, err
	}
	switch resolved {
	case "hetzner":
		return hetznerListImages(ctx)
	case "digitalocean":
		return digitalOceanListImages(ctx)
	case "runpod":
		return runPodListImages(ctx)
	default:
		if isAPIProvider(resolved) {
			return apiProviderListImages(ctx, resolved)
		}
		return nil, providerAdapterUnavailable(resolved, "image catalog")
	}
}

// ─── Hetzner adapters ──────────────────────────────────────────────

func hetznerListServerTypes(ctx *sdk.AppCtx) ([]ServerType, error) {
	bound, err := instanceProviderBinding(ctx, "hetzner")
	if err != nil {
		return nil, err
	}
	// per_page=50 hits Hetzner's max for server_types; the current
	// catalog is well under that, so one page covers everything.
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "server_types_list", map[string]any{
		"per_page": 50,
	})
	if err != nil {
		return nil, fmt.Errorf("server_types_list: %w", err)
	}
	if res == nil || !res.Success {
		return nil, fmt.Errorf("server_types_list: %s", upstreamErrorString(res))
	}
	types, err := parseHetznerServerTypes(res.Data)
	if err != nil {
		return nil, err
	}
	return activeServerTypes(types), nil
}

func activeServerTypes(types []ServerType) []ServerType {
	out := make([]ServerType, 0, len(types))
	for _, t := range types {
		if !t.Deprecated {
			out = append(out, t)
		}
	}
	return out
}

func hetznerListLocations(ctx *sdk.AppCtx) ([]Location, error) {
	bound, err := instanceProviderBinding(ctx, "hetzner")
	if err != nil {
		return nil, err
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "locations_list", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("locations_list: %w", err)
	}
	if res == nil || !res.Success {
		return nil, fmt.Errorf("locations_list: %s", upstreamErrorString(res))
	}
	return parseHetznerLocations(res.Data)
}

func hetznerListImages(ctx *sdk.AppCtx) ([]Image, error) {
	bound, err := instanceProviderBinding(ctx, "hetzner")
	if err != nil {
		return nil, err
	}
	// type=system narrows to the OS images we'd boot a fresh server
	// from. Excludes snapshots/backups/app images which aren't
	// relevant to provisioning. status=available skips images still
	// being created.
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "images_list", map[string]any{
		"type":     "system",
		"status":   "available",
		"per_page": 50,
	})
	if err != nil {
		return nil, fmt.Errorf("images_list: %w", err)
	}
	if res == nil || !res.Success {
		return nil, fmt.Errorf("images_list: %s", upstreamErrorString(res))
	}
	return parseHetznerImages(res.Data)
}

// ─── Hetzner response parsers ──────────────────────────────────────

// parseHetznerServerTypes pulls the `server_types` array out of
// Hetzner's response. Each type carries pricing per location; we
// pick the first location's monthly + hourly EUR rate (they're
// identical across Hetzner locations today; we still copy the
// per-location list into AvailableIn so panels can filter).
func parseHetznerServerTypes(data json.RawMessage) ([]ServerType, error) {
	var v struct {
		ServerTypes []struct {
			Name         string  `json:"name"`
			Description  string  `json:"description"`
			Cores        int     `json:"cores"`
			Memory       float64 `json:"memory"`
			Disk         int     `json:"disk"`
			CPUType      string  `json:"cpu_type"`
			Architecture string  `json:"architecture"`
			Deprecated   bool    `json:"deprecated"`
			Prices       []struct {
				Location     string `json:"location"`
				PriceMonthly struct {
					Gross string `json:"gross"`
				} `json:"price_monthly"`
				PriceHourly struct {
					Gross string `json:"gross"`
				} `json:"price_hourly"`
			} `json:"prices"`
			Locations []struct {
				Name      string `json:"name"`
				Available bool   `json:"available"`
			} `json:"locations"`
		} `json:"server_types"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("decode server_types: %w", err)
	}
	out := make([]ServerType, 0, len(v.ServerTypes))
	for _, st := range v.ServerTypes {
		row := ServerType{
			Name:         st.Name,
			Description:  st.Description,
			Cores:        st.Cores,
			MemoryGB:     st.Memory,
			DiskGB:       st.Disk,
			CPUType:      st.CPUType,
			Architecture: st.Architecture,
			Deprecated:   st.Deprecated,
		}
		row.AvailableIn = make([]string, 0, len(st.Locations))
		for _, loc := range st.Locations {
			if loc.Available {
				row.AvailableIn = append(row.AvailableIn, loc.Name)
			}
		}
		if len(row.AvailableIn) == 0 && len(st.Locations) == 0 {
			row.AvailableIn = make([]string, 0, len(st.Prices))
			for _, p := range st.Prices {
				row.AvailableIn = append(row.AvailableIn, p.Location)
			}
		}
		for i, p := range st.Prices {
			if i == 0 {
				row.MonthlyPriceEUR = parseHetznerPriceString(p.PriceMonthly.Gross)
				row.HourlyPriceEUR = parseHetznerPriceString(p.PriceHourly.Gross)
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func parseHetznerLocations(data json.RawMessage) ([]Location, error) {
	var v struct {
		Locations []struct {
			Name        string  `json:"name"`
			City        string  `json:"city"`
			Country     string  `json:"country"`
			Description string  `json:"description"`
			NetworkZone string  `json:"network_zone"`
			Latitude    float64 `json:"latitude"`
			Longitude   float64 `json:"longitude"`
		} `json:"locations"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("decode locations: %w", err)
	}
	out := make([]Location, 0, len(v.Locations))
	for _, loc := range v.Locations {
		out = append(out, Location{
			Name: loc.Name, City: loc.City, Country: loc.Country,
			Description: loc.Description, NetworkZone: loc.NetworkZone,
			Latitude: loc.Latitude, Longitude: loc.Longitude,
		})
	}
	return out, nil
}

func parseHetznerImages(data json.RawMessage) ([]Image, error) {
	var v struct {
		Images []struct {
			Name         string `json:"name"`
			Description  string `json:"description"`
			OSFlavor     string `json:"os_flavor"`
			OSVersion    string `json:"os_version"`
			Architecture string `json:"architecture"`
			DiskSize     int    `json:"disk_size"`
		} `json:"images"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("decode images: %w", err)
	}
	out := make([]Image, 0, len(v.Images))
	for _, im := range v.Images {
		// Hetzner gives every system image a stable `name` (e.g.
		// "ubuntu-24.04") AND a numeric id. We expose name only —
		// it's what server_create accepts, what panels show, and
		// what survives Hetzner re-numbering.
		if im.Name == "" {
			continue
		}
		out = append(out, Image{
			Name: im.Name, Description: im.Description,
			OSFlavor: im.OSFlavor, OSVersion: im.OSVersion,
			Architecture: im.Architecture, DiskSizeGB: im.DiskSize,
		})
	}
	return out, nil
}

// ─── helpers ───────────────────────────────────────────────────────

// parseHetznerPriceString turns Hetzner's stringy decimal price
// ("3.7900000000" or "0.0063") into a float64. Unparseable values
// fall through to 0 so panels render "—" rather than crash.
func parseHetznerPriceString(s string) float64 {
	if s == "" {
		return 0
	}
	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err != nil {
		return 0
	}
	return f
}
