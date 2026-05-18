package main

// Live catalog queries — server types, locations, OS images — pulled
// from the bound VPS provider in real time rather than hardcoded in
// the manifest, the panel, or the user's head. Hetzner ages out
// server types (cpx* deprecated mid-2025), adds new locations
// (Singapore, US-East), and rolls forward OS images monthly. Anything
// we baked into a select-options list would be stale within months.
//
// Shape is provider-agnostic so DO/Vultr/AWS slot in later: the
// returned rows carry name + display + capacity + price fields that
// every IaaS exposes in some form. Hetzner's own shape lands as the
// source of truth for v0.3; future providers will normalize into
// this same envelope.
//
// No caching for v0.3 — Hetzner has generous rate limits and the
// panel only calls these on dialog-open. If/when a worker starts
// hammering them we can add a 5-min in-memory cache without
// changing the call sites.

import (
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

// ─── output shapes ─────────────────────────────────────────────────

// ServerType is the per-call shape returned to MCP + HTTP callers.
// Fields that don't apply to a given provider stay zero.
type ServerType struct {
	Name           string         `json:"name"`
	Description    string         `json:"description,omitempty"`
	Cores          int            `json:"cores"`
	MemoryGB       float64        `json:"memory_gb"`
	DiskGB         int            `json:"disk_gb"`
	CPUType        string         `json:"cpu_type,omitempty"`     // shared | dedicated
	Architecture   string         `json:"architecture,omitempty"` // x86 | arm
	Deprecated     bool           `json:"deprecated,omitempty"`
	MonthlyPriceEUR float64       `json:"monthly_price_eur,omitempty"`
	HourlyPriceEUR  float64       `json:"hourly_price_eur,omitempty"`
	// AvailableIn lists location names where this type can be
	// provisioned. Hetzner ships some types only in newer regions.
	AvailableIn []string `json:"available_in,omitempty"`
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
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	OSFlavor     string `json:"os_flavor,omitempty"`  // ubuntu | debian | …
	OSVersion    string `json:"os_version,omitempty"` // 24.04 | 12 | …
	Architecture string `json:"architecture,omitempty"`
	DiskSizeGB   int    `json:"disk_size_gb,omitempty"`
}

// ─── entry points ──────────────────────────────────────────────────

func listServerTypes(ctx *sdk.AppCtx, provider string) ([]ServerType, error) {
	switch normalizeProvider(provider) {
	case "hetzner":
		return hetznerListServerTypes(ctx)
	default:
		return nil, fmt.Errorf("provider %q not supported (v0.3 ships only 'hetzner')", provider)
	}
}

func listLocations(ctx *sdk.AppCtx, provider string) ([]Location, error) {
	switch normalizeProvider(provider) {
	case "hetzner":
		return hetznerListLocations(ctx)
	default:
		return nil, fmt.Errorf("provider %q not supported (v0.3 ships only 'hetzner')", provider)
	}
}

func listImages(ctx *sdk.AppCtx, provider string) ([]Image, error) {
	switch normalizeProvider(provider) {
	case "hetzner":
		return hetznerListImages(ctx)
	default:
		return nil, fmt.Errorf("provider %q not supported (v0.3 ships only 'hetzner')", provider)
	}
}

// ─── Hetzner adapters ──────────────────────────────────────────────

func hetznerListServerTypes(ctx *sdk.AppCtx) ([]ServerType, error) {
	bound := ctx.IntegrationFor("provider")
	if bound == nil || bound.ConnectionID == 0 {
		return nil, errors.New("no VPS provider bound — bind a Hetzner connection on the Instances install to load the live catalog")
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
	return parseHetznerServerTypes(res.Data)
}

func hetznerListLocations(ctx *sdk.AppCtx) ([]Location, error) {
	bound := ctx.IntegrationFor("provider")
	if bound == nil || bound.ConnectionID == 0 {
		return nil, errors.New("no VPS provider bound")
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
	bound := ctx.IntegrationFor("provider")
	if bound == nil || bound.ConnectionID == 0 {
		return nil, errors.New("no VPS provider bound")
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
		row.AvailableIn = make([]string, 0, len(st.Prices))
		for i, p := range st.Prices {
			row.AvailableIn = append(row.AvailableIn, p.Location)
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

// normalizeProvider lowercases + defaults empty to the only v0.3
// provider. Future providers add cases in the listX entry points.
func normalizeProvider(p string) string {
	switch p {
	case "":
		return "hetzner"
	default:
		return p
	}
}

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
