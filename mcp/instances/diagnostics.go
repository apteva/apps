package main

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type ProviderComparison struct {
	InstanceID     int64          `json:"instance_id"`
	CheckedAt      string         `json:"checked_at"`
	ProviderExists bool           `json:"provider_exists"`
	ProviderState  string         `json:"provider_state,omitempty"`
	ProviderIPv4   string         `json:"provider_ipv4,omitempty"`
	ProviderIPv6   string         `json:"provider_ipv6,omitempty"`
	Resources      map[string]any `json:"resources,omitempty"`
	Differences    []string       `json:"differences"`
}

func compareInstanceProvider(ctx *sdk.AppCtx, inst *Instance) (*ProviderComparison, error) {
	if inst == nil {
		return nil, ErrInstanceNotFound
	}
	result := &ProviderComparison{InstanceID: inst.ID, CheckedAt: nowUTC(), Resources: map[string]any{}, Differences: []string{}}
	if inst.IsLocal() || inst.Provider == "external" {
		result.ProviderExists = true
		result.ProviderState = inst.Status
		return result, nil
	}
	var data json.RawMessage
	var err error
	if isAPIProvider(inst.Provider) {
		tool, args, requestErr := apiProviderGetRequest(inst)
		if requestErr != nil {
			return nil, requestErr
		}
		data, err = executeProviderToolOnConnection(ctx, inst.ProviderConnectionID, inst.Provider, tool, args)
	} else {
		bound, bindErr := instanceProviderBinding(ctx, inst.Provider)
		if bindErr != nil {
			return nil, bindErr
		}
		tool, args := "", map[string]any{}
		switch inst.Provider {
		case "hetzner":
			tool, args = "server_get", map[string]any{"id": atoiAny(inst.ProviderID)}
		case "digitalocean":
			tool, args = "get_droplet", map[string]any{"droplet_id": atoiAny(inst.ProviderID)}
		default:
			return nil, providerAdapterUnavailable(inst.Provider, "provider comparison")
		}
		data, err = executeProviderToolOnConnection(ctx, bound.ConnectionID, inst.Provider, tool, args)
	}
	if err != nil {
		if strings.Contains(err.Error(), "status=404") {
			result.Differences = append(result.Differences, "provider resource is missing")
			persistProviderComparison(ctx, inst.ID, result)
			return result, nil
		}
		return nil, err
	}
	result.ProviderExists = true
	result.ProviderState = firstNonEmpty(findJSONScalar(data, "state"), findJSONScalar(data, "status"))
	_, result.ProviderIPv4, result.ProviderIPv6 = parseProviderResource(inst.Provider, data)
	if result.ProviderIPv4 == "" {
		result.ProviderIPv4 = firstNonEmpty(findJSONScalar(data, "public_ipv4"), findJSONScalar(data, "address"))
	}
	if inst.PublicIPv4 != "" && result.ProviderIPv4 != "" && inst.PublicIPv4 != result.ProviderIPv4 {
		result.Differences = append(result.Differences, fmt.Sprintf("IPv4 differs: app=%s provider=%s", inst.PublicIPv4, result.ProviderIPv4))
	}
	if inst.Status == "ready" && strings.EqualFold(result.ProviderState, "stopped") {
		result.Differences = append(result.Differences, "app is ready but provider reports stopped")
	}
	volumes, _ := dbListVolumes(ctx.AppDB(), inst.ID, "")
	result.Resources["server_id"] = inst.ProviderID
	result.Resources["volumes"] = volumes
	persistProviderComparison(ctx, inst.ID, result)
	return result, nil
}

func persistProviderComparison(ctx *sdk.AppCtx, id int64, comparison *ProviderComparison) {
	encoded, err := json.Marshal(comparison)
	if err == nil {
		_ = dbUpdateInstance(ctx.AppDB(), id, map[string]any{"provider_inventory_json": string(encoded), "provider_checked_at": comparison.CheckedAt})
	}
}

func scalewayProviderInventory(ctx *sdk.AppCtx, connectionID int64) (map[string]any, error) {
	if connectionID == 0 {
		bound, err := instanceProviderBinding(ctx, "scaleway")
		if err != nil {
			return nil, err
		}
		connectionID = bound.ConnectionID
	}
	zones := []string{"fr-par-1", "fr-par-2", "nl-ams-1", "nl-ams-2", "pl-waw-2", "pl-waw-3"}
	trackedInstances := map[string]bool{}
	if instances, err := dbListInstances(ctx.AppDB(), "scaleway", ""); err == nil {
		for _, instance := range instances {
			if instance.ProviderConnectionID == connectionID && instance.ProviderID != "" {
				trackedInstances[instance.ProviderID] = true
			}
		}
	}
	trackedVolumes := map[string]bool{}
	if volumes, err := dbListVolumes(ctx.AppDB(), 0, "scaleway"); err == nil {
		for _, volume := range volumes {
			if volume.ProviderConnectionID == connectionID && volume.ProviderVolumeID != "" {
				trackedVolumes[volume.ProviderVolumeID] = true
			}
		}
	}
	objectStorage, _ := dbListObjectStorages(ctx.AppDB(), "scaleway")
	trackedBuckets := []map[string]any{}
	trackedBucketNames := map[string]bool{}
	for _, item := range objectStorage {
		if item.ProviderConnectionID == connectionID {
			trackedBuckets = append(trackedBuckets, map[string]any{"id": item.ID, "bucket": item.Bucket, "region": item.Region, "status": item.Status})
			trackedBucketNames[item.Bucket] = true
		}
	}
	result := map[string]any{"provider": "scaleway", "connection_id": connectionID, "checked_at": nowUTC(), "zones": map[string]any{}, "tracked_object_storage": trackedBuckets}
	zoneResults := result["zones"].(map[string]any)
	orphanServers, orphanVolumes := []string{}, []string{}
	for _, zone := range zones {
		entry := map[string]any{}
		resources := []struct{ label, tool, array string }{
			{"instances", "server_list", "servers"},
			{"volumes", "volume_list", "volumes"},
			{"elastic_metal", "elastic_metal_servers_list", "servers"},
			{"flexible_ips", "elastic_metal_flexible_ips_list", "flexible_ips"},
		}
		for _, resource := range resources {
			pageArg := "page_size"
			if resource.tool == "volume_list" {
				pageArg = "per_page"
			}
			data, err := executeProviderToolOnConnection(ctx, connectionID, "scaleway", resource.tool, map[string]any{"zone": zone, pageArg: 100})
			if err != nil {
				entry[resource.label+"_error"] = err.Error()
				continue
			}
			var decoded any
			if json.Unmarshal(data, &decoded) == nil {
				entry[resource.label] = decoded
				for _, item := range namedResourceMaps(decoded, resource.array) {
					id := mapString(item, "id")
					if id == "" {
						continue
					}
					if (resource.label == "instances" || resource.label == "elastic_metal") && !trackedInstances[id] {
						orphanServers = append(orphanServers, id)
					}
					if resource.label == "volumes" && !trackedVolumes[id] {
						orphanVolumes = append(orphanVolumes, id)
					}
				}
			}
		}
		zoneResults[zone] = entry
	}
	result["orphan_server_ids"] = orphanServers
	result["orphan_volume_ids"] = orphanVolumes
	providerBuckets := map[string][]string{}
	orphanBuckets, seenBuckets := []string{}, map[string]bool{}
	for _, region := range []string{"fr-par", "nl-ams", "pl-waw", "it-mil"} {
		data, err := executeProviderToolOnConnection(ctx, connectionID, "scaleway", "object_bucket_list", map[string]any{"region": region})
		if err != nil {
			providerBuckets[region] = []string{}
			continue
		}
		names := scalewayBucketNames(data)
		providerBuckets[region] = names
		for _, name := range names {
			if !trackedBucketNames[name] && !seenBuckets[name] {
				orphanBuckets = append(orphanBuckets, name)
			}
			seenBuckets[name] = true
		}
	}
	result["provider_object_storage"] = providerBuckets
	result["orphan_bucket_names"] = orphanBuckets
	return result, nil
}

func scalewayBucketNames(data json.RawMessage) []string {
	var body string
	if json.Unmarshal(data, &body) != nil {
		body = string(data)
	}
	var response struct {
		Buckets []struct {
			Name string `xml:"Name"`
		} `xml:"Buckets>Bucket"`
	}
	if xml.Unmarshal([]byte(body), &response) != nil {
		return []string{}
	}
	out := make([]string, 0, len(response.Buckets))
	for _, bucket := range response.Buckets {
		if bucket.Name != "" {
			out = append(out, bucket.Name)
		}
	}
	return out
}

func namedResourceMaps(value any, key string) []map[string]any {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	items, ok := root[key].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}

func benchmarkInstanceStorage(ctx *sdk.AppCtx, inst *Instance, target string) (map[string]any, error) {
	if inst == nil {
		return nil, ErrInstanceNotFound
	}
	if inst.Status != "ready" {
		return nil, fmt.Errorf("instance not ready (status=%s)", inst.Status)
	}
	target = strings.TrimSpace(target)
	if target == "" {
		target = "/"
	}
	if !strings.HasPrefix(target, "/") || strings.Contains(target, "\x00") {
		return nil, errors.New("target_path must be an absolute path")
	}
	file := strings.TrimRight(target, "/") + "/.apteva-storage-benchmark"
	cmd := "set -eu; test -d " + quoteShellArg(target) + "; trap 'rm -f " + quoteShellArg(file) + "' EXIT; dd if=/dev/zero of=" + quoteShellArg(file) + " bs=1M count=256 conv=fdatasync 2>&1"
	started := time.Now()
	var output string
	var exit int
	var err error
	if inst.IsLocal() {
		output, exit, err = runLocal(cmd, 2*time.Minute)
	} else {
		output, exit, err = runSSH(inst, cmd, 2*time.Minute)
	}
	result := map[string]any{"target_path": target, "bytes": 268435456, "elapsed_seconds": time.Since(started).Seconds(), "output": strings.TrimSpace(output), "exit_code": exit, "measured_at": nowUTC()}
	encoded, _ := json.Marshal(result)
	_, _ = ctx.AppDB().Exec(`INSERT INTO instance_storage_benchmarks(instance_id,target_path,result_json) VALUES(?,?,?)`, inst.ID, target, string(encoded))
	if err != nil || exit != 0 {
		return result, fmt.Errorf("storage benchmark failed (exit=%d): %w", exit, err)
	}
	return result, nil
}

func reconcileTrackedProviderState(ctx *sdk.AppCtx) {
	rows, err := dbListInstances(ctx.AppDB(), "", "")
	if err != nil {
		return
	}
	for _, inst := range rows {
		if inst.IsLocal() || inst.Provider == "external" || inst.ProviderID == "" || (inst.Status != "ready" && inst.Status != "error") {
			continue
		}
		comparison, compareErr := compareInstanceProvider(ctx, inst)
		if compareErr != nil {
			ctx.Logger().Warn("instances: provider reconciliation failed", "id", inst.ID, "provider", inst.Provider, "err", compareErr)
			continue
		}
		if !comparison.ProviderExists && inst.Status == "ready" {
			message := "provider reconciliation: resource no longer exists upstream"
			_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{"status": "error", "lifecycle_stage": "Reconcile", "primary_error": message, "error_message": message})
		}
	}
}

func (a *App) toolCompareProvider(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	inst, err := dbGetInstance(ctx.AppDB(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	comparison, err := compareInstanceProvider(ctx, inst)
	if err != nil {
		return nil, err
	}
	return map[string]any{"comparison": comparison}, nil
}

func (a *App) toolProviderInventory(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	provider, err := resolveInstanceProvider(ctx, strArg(args, "provider"))
	if err != nil {
		return nil, err
	}
	if provider != "scaleway" {
		return nil, providerAdapterUnavailable(provider, "full resource inventory")
	}
	return scalewayProviderInventory(ctx, int64Arg(args, "provider_connection_id"))
}

func (a *App) toolStorageBenchmark(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	inst, err := dbGetInstance(ctx.AppDB(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	result, err := benchmarkInstanceStorage(ctx, inst, strArg(args, "target_path"))
	if err != nil {
		return map[string]any{"result": result}, err
	}
	return map[string]any{"result": result}, nil
}
