package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func digitalOceanBound(ctx *sdk.AppCtx) (*sdk.BoundIntegration, error) {
	return instanceProviderBinding(ctx, "digitalocean")
}

func digitalOceanListServerTypes(ctx *sdk.AppCtx) ([]ServerType, error) {
	data, err := executeProviderTool(ctx, "digitalocean", "list_sizes", map[string]any{"per_page": 200})
	if err != nil {
		return nil, err
	}
	return parseDigitalOceanSizes(data)
}

func digitalOceanListLocations(ctx *sdk.AppCtx) ([]Location, error) {
	data, err := executeProviderTool(ctx, "digitalocean", "list_regions", map[string]any{"per_page": 200})
	if err != nil {
		return nil, err
	}
	return parseDigitalOceanRegions(data)
}

func digitalOceanListImages(ctx *sdk.AppCtx) ([]Image, error) {
	data, err := executeProviderTool(ctx, "digitalocean", "list_images", map[string]any{"per_page": 200, "type": "distribution"})
	if err != nil {
		return nil, err
	}
	return parseDigitalOceanImages(data)
}

func digitalOceanProvision(ctx *sdk.AppCtx, in CreateInstanceInput) (*Instance, error) {
	bound, err := storageBinding(ctx, "digitalocean", in.ProviderConnectionID)
	if err != nil {
		return nil, err
	}

	privKey, pubKey, err := generateSSHKeypair()
	if err != nil {
		return nil, fmt.Errorf("generate ssh keypair: %w", err)
	}
	in.SSHPrivateKey = privKey
	in.SSHPublicKey = pubKey
	in.SSHUser = "root"
	if in.Image == "" {
		in.Image = "ubuntu-24-04-x64"
	}
	if in.Size == "" {
		in.Size = "s-1vcpu-1gb"
	}
	if in.Region == "" {
		in.Region = "nyc1"
	}
	in.Provider = "digitalocean"
	in.Status = "provisioning"

	if in.MonthlyCostCents == 0 {
		in.MonthlyCostCents = digitalOceanSizeMonthlyCostCents(ctx, in.Size)
	}

	inst, err := dbCreateInstance(ctx.AppDB(), in)
	if err != nil {
		return nil, err
	}
	defer trackInstanceCreation(ctx, inst.ID)()
	emitInstanceCreated(ctx, inst)
	emitInstanceStatus(ctx, inst)

	args := map[string]any{
		"name":      in.Name,
		"region":    in.Region,
		"size":      in.Size,
		"image":     in.Image,
		"user_data": buildCloudInit(pubKey),
		"backups":   false,
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "create_droplet", args)
	if err != nil {
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{
			"status":        "error",
			"error_message": fmt.Sprintf("digitalocean.create_droplet: %v", err),
		})
		return nil, fmt.Errorf("digitalocean.create_droplet: %w", err)
	}
	if res == nil || !res.Success {
		msg := upstreamErrorString(res)
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{
			"status":        "error",
			"error_message": msg,
		})
		return nil, fmt.Errorf("digitalocean.create_droplet returned status=%d: %s", upstreamStatus(res), msg)
	}

	provID, ipv4, ipv6 := parseDigitalOceanDropletResponse(res.Data)
	if provID == "" {
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{
			"status":        "error",
			"error_message": "digitalocean.create_droplet response missing droplet id; catalog shape may be out of sync with upstream API",
		})
		return nil, errors.New("digitalocean.create_droplet response missing droplet id")
	}
	if err := dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{
		"provider_id": provID,
		"public_ipv4": ipv4,
		"public_ipv6": ipv6,
	}); err != nil {
		orphan := *inst
		orphan.ProviderID, orphan.PublicIPv4, orphan.PublicIPv6 = provID, ipv4, ipv6
		cleanupErr := digitalOceanDestroy(ctx, &orphan)
		ctx.Logger().Error("instances: failed to persist DigitalOcean identity", "id", inst.ID, "provider_id", provID, "err", err, "cleanup_err", cleanupErr)
		if cleanupErr != nil {
			return nil, fmt.Errorf("persist created DigitalOcean droplet %s: %w; automatic cleanup also failed: %v", provID, err, cleanupErr)
		}
		return nil, fmt.Errorf("persist created DigitalOcean droplet %s: %w; upstream droplet was cleaned up", provID, err)
	}

	kickDigitalOceanReadinessProbe(ctx, inst.ID)
	return dbGetInstance(ctx.AppDB(), inst.ID)
}

func digitalOceanDestroy(ctx *sdk.AppCtx, inst *Instance) error {
	bound, err := storageBinding(ctx, "digitalocean", inst.ProviderConnectionID)
	if err != nil {
		return err
	}
	if inst.ProviderID == "" {
		return nil
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "delete_droplet", map[string]any{
		"droplet_id": inst.ProviderID,
	})
	if err != nil {
		return fmt.Errorf("digitalocean.delete_droplet: %w", err)
	}
	if res == nil || !res.Success {
		if upstreamStatus(res) == 404 {
			return nil
		}
		return fmt.Errorf("digitalocean.delete_droplet returned: %s", upstreamErrorString(res))
	}
	return nil
}

func kickDigitalOceanReadinessProbe(ctx *sdk.AppCtx, id int64) {
	if !finishInstanceCreation(ctx, id) {
		return
	}
	probe := probeSSHReadyFn
	startInstanceWorker(ctx, id, func(work context.Context) {
		fresh, err := dbGetInstance(ctx.AppDB(), id)
		if err != nil {
			return
		}
		if fresh.Status != "provisioning" {
			return
		}
		if fresh.PublicIPv4 == "" && fresh.PublicIPv6 == "" {
			fresh, err = waitDigitalOceanDropletNetwork(ctx, fresh, 5*time.Minute)
			if err != nil {
				_, _ = updateInstanceAndEmit(ctx, id, map[string]any{
					"status":        "error",
					"error_message": fmt.Sprintf("digitalocean network: %v", err),
				})
				return
			}
		}
		fresh.workContext = work
		if err := probe(fresh, 5*time.Minute); err != nil {
			_, _ = updateInstanceAndEmit(ctx, id, map[string]any{
				"status":        "error",
				"error_message": fmt.Sprintf("ssh probe: %v", err),
			})
			return
		}
		if work.Err() != nil {
			return
		}
		_, _, _ = transitionInstanceAndEmit(ctx, id, []string{"provisioning"}, "ready", map[string]any{"ready_at": nowUTC(), "error_message": ""})
	})
}

func waitDigitalOceanDropletNetwork(ctx *sdk.AppCtx, inst *Instance, timeout time.Duration) (*Instance, error) {
	bound, err := storageBinding(ctx, "digitalocean", inst.ProviderConnectionID)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "get_droplet", map[string]any{
			"droplet_id": inst.ProviderID,
		})
		if err != nil {
			return nil, fmt.Errorf("get_droplet: %w", err)
		}
		if res == nil || !res.Success {
			return nil, fmt.Errorf("get_droplet: %s", upstreamErrorString(res))
		}
		_, ipv4, ipv6 := parseDigitalOceanDropletResponse(res.Data)
		if ipv4 != "" || ipv6 != "" {
			_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{
				"public_ipv4": ipv4,
				"public_ipv6": ipv6,
			})
			return dbGetInstance(ctx.AppDB(), inst.ID)
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out waiting for public IP")
		}
		if err := sleepContext(instanceWorkerContext(ctx, inst.ID), 5*time.Second); err != nil {
			return nil, err
		}
	}
}

func reconcileDigitalOceanProvisioning(ctx *sdk.AppCtx) {
	rows, err := dbListInstances(ctx.AppDB(), "digitalocean", "provisioning")
	if err != nil {
		ctx.Logger().Warn("instances: reconcile digitalocean list failed", "err", err)
		return
	}
	for _, inst := range rows {
		if inst.ProviderID != "" {
			kickDigitalOceanReadinessProbe(ctx, inst.ID)
			continue
		}
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{
			"status":        "error",
			"error_message": "provisioning interrupted before DigitalOcean droplet id was recorded — Instances will not infer a droplet by name; check the DigitalOcean dashboard for an orphan droplet named " + inst.Name,
		})
	}
}

func parseDigitalOceanSizes(data json.RawMessage) ([]ServerType, error) {
	var v struct {
		Sizes []struct {
			Slug         string   `json:"slug"`
			Memory       int      `json:"memory"`
			VCPUs        int      `json:"vcpus"`
			Disk         int      `json:"disk"`
			Transfer     float64  `json:"transfer"`
			PriceMonthly float64  `json:"price_monthly"`
			PriceHourly  float64  `json:"price_hourly"`
			Regions      []string `json:"regions"`
			Available    bool     `json:"available"`
			Description  string   `json:"description"`
		} `json:"sizes"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("decode digitalocean sizes: %w", err)
	}
	out := make([]ServerType, 0, len(v.Sizes))
	for _, size := range v.Sizes {
		if size.Slug == "" || !size.Available {
			continue
		}
		desc := size.Description
		if desc == "" {
			desc = size.Slug
		}
		out = append(out, ServerType{
			Name:            size.Slug,
			Description:     desc,
			Cores:           size.VCPUs,
			MemoryGB:        float64(size.Memory) / 1024,
			DiskGB:          size.Disk,
			CPUType:         "shared",
			Architecture:    "x86",
			MonthlyPriceUSD: size.PriceMonthly,
			HourlyPriceUSD:  size.PriceHourly,
			AvailableIn:     append([]string(nil), size.Regions...),
		})
	}
	return out, nil
}

func parseDigitalOceanRegions(data json.RawMessage) ([]Location, error) {
	var v struct {
		Regions []struct {
			Slug      string `json:"slug"`
			Name      string `json:"name"`
			Available bool   `json:"available"`
		} `json:"regions"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("decode digitalocean regions: %w", err)
	}
	out := make([]Location, 0, len(v.Regions))
	for _, region := range v.Regions {
		if region.Slug == "" || !region.Available {
			continue
		}
		out = append(out, Location{
			Name:        region.Slug,
			Description: region.Name,
			City:        digitalOceanRegionCity(region.Name),
		})
	}
	return out, nil
}

func parseDigitalOceanImages(data json.RawMessage) ([]Image, error) {
	var v struct {
		Images []struct {
			ID           any    `json:"id"`
			Name         string `json:"name"`
			Slug         string `json:"slug"`
			Distribution string `json:"distribution"`
			Public       bool   `json:"public"`
			Type         string `json:"type"`
			MinDiskSize  int    `json:"min_disk_size"`
		} `json:"images"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("decode digitalocean images: %w", err)
	}
	out := make([]Image, 0, len(v.Images))
	for _, im := range v.Images {
		if !im.Public || im.Type != "base" {
			continue
		}
		name := im.Slug
		if name == "" {
			name = anyToString(im.ID)
		}
		if name == "" {
			continue
		}
		out = append(out, Image{
			Name:        name,
			Description: strings.TrimSpace(strings.Join([]string{im.Distribution, im.Name}, " ")),
			OSFlavor:    strings.ToLower(im.Distribution),
			OSVersion:   digitalOceanImageVersion(im.Name),
			DiskSizeGB:  im.MinDiskSize,
		})
	}
	return out, nil
}

func parseDigitalOceanDropletResponse(data json.RawMessage) (id, ipv4, ipv6 string) {
	var v struct {
		Droplet struct {
			ID       any `json:"id"`
			Networks struct {
				V4 []struct {
					IPAddress string `json:"ip_address"`
					Type      string `json:"type"`
				} `json:"v4"`
				V6 []struct {
					IPAddress string `json:"ip_address"`
					Type      string `json:"type"`
				} `json:"v6"`
			} `json:"networks"`
		} `json:"droplet"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return "", "", ""
	}
	id = anyToString(v.Droplet.ID)
	for _, net := range v.Droplet.Networks.V4 {
		if net.Type == "public" && net.IPAddress != "" {
			ipv4 = net.IPAddress
			break
		}
	}
	for _, net := range v.Droplet.Networks.V6 {
		if net.Type == "public" && net.IPAddress != "" {
			ipv6 = net.IPAddress
			break
		}
	}
	return id, ipv4, ipv6
}

func digitalOceanSizeMonthlyCostCents(ctx *sdk.AppCtx, size string) int {
	types, err := digitalOceanListServerTypes(ctx)
	if err != nil {
		return 0
	}
	for _, t := range types {
		if t.Name == size && t.MonthlyPriceUSD > 0 {
			return int(t.MonthlyPriceUSD * 100)
		}
	}
	return 0
}

func anyToString(v any) string {
	switch n := v.(type) {
	case string:
		return n
	case float64:
		return strconv.FormatInt(int64(n), 10)
	case int:
		return strconv.Itoa(n)
	case int64:
		return strconv.FormatInt(n, 10)
	case json.Number:
		return n.String()
	default:
		return ""
	}
}

func digitalOceanRegionCity(name string) string {
	if i := strings.Index(name, " "); i > 0 {
		return name[:i]
	}
	return name
}

func digitalOceanImageVersion(name string) string {
	fields := strings.Fields(name)
	for _, field := range fields {
		if strings.Contains(field, ".") {
			return strings.Trim(field, "(),")
		}
	}
	return ""
}
