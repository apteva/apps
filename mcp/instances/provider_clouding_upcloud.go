package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Provider catalogs are fetched through the bound integration, never directly
// with provider credentials. UpCloud prices vary by zone; absent verified quotes
// remain unknown. Clouding flavor prices exclude disks and image licenses.
func cloudingUpcloudServerTypes(ctx *sdk.AppCtx, provider string) ([]ServerType, error) {
	tool := "list_plans"
	if provider == "clouding" {
		tool = "list_flavors"
	}
	args := map[string]any{}
	if provider == "clouding" {
		args = map[string]any{"page": 1, "pageSize": 200}
	}
	data, err := executeProviderTool(ctx, provider, tool, args)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err = json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	var out []ServerType
	var rows []map[string]any
	if provider == "clouding" {
		rows = collectMaps(root["flavors"])
	} else {
		rows = collectMaps(nestedValue(root, "plans", "plan"))
	}
	for _, row := range rows {
		t := ServerType{Architecture: "x86", Platform: "linux", ResourceClass: "virtual", CPUType: "shared", PriceKnown: boolPtr(false)}
		if provider == "clouding" {
			t.Name = mapString(row, "id")
			t.VCPUs = numberValue(row["vCores"])
			t.Cores = int(t.VCPUs)
			t.MemoryGB = numberValue(row["ramGb"])
			t.Description = fmt.Sprintf("%g vCPU / %g GB RAM; disk billed separately", t.VCPUs, t.MemoryGB)
			t.AvailableIn = []string{"barcelona"}
			t.ConfigurableBoot = boolPtr(true)
			t.RequiresBootStorage = true
		} else {
			t.Name = mapString(row, "name")
			t.Cores = anyToInt(row["core_number"])
			t.MemoryGB = numberValue(row["memory_amount"]) / 1024
			t.DiskGB = anyToInt(row["storage_size"])
			t.Description = t.Name
			// GPU and diskless plans need additional placement/storage contracts.
			// Exclude them until those requirements are supported by this adapter.
			if anyToInt(row["gpu_amount"]) > 0 || t.DiskGB <= 0 || mapString(row, "storage_tier") != "maxiops" {
				continue
			}
			t.ConfigurableBoot = boolPtr(true)
		}
		if t.Name == "" || t.MemoryGB <= 0 || (provider == "clouding" && t.VCPUs <= 0) {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func numberValue(value any) float64 {
	switch n := value.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		var f float64
		fmt.Sscan(n, &f)
		return f
	}
	return 0
}

func cloudingUpcloudLocations(ctx *sdk.AppCtx, provider string) ([]Location, error) {
	if provider == "clouding" {
		// Clouding's API has one physical site and accepts no region parameter.
		return []Location{{Name: "barcelona", City: "Barcelona", Country: "ES", Description: "Barcelona, Spain", NetworkZone: "barcelona"}}, nil
	}
	data, err := executeProviderTool(ctx, provider, "list_zones", map[string]any{})
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err = json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	out := []Location{}
	for _, row := range collectMaps(nestedValue(root, "zones", "zone")) {
		id := mapString(row, "id")
		if id == "" || mapString(row, "public") != "yes" {
			continue
		}
		country := ""
		if len(id) >= 2 {
			country = strings.ToUpper(id[:2])
		}
		out = append(out, Location{Name: id, Country: country, Description: mapString(row, "description"), NetworkZone: id})
	}
	return out, nil
}

func cloudingUpcloudImages(ctx *sdk.AppCtx, provider string) ([]Image, error) {
	args := map[string]any{}
	if provider == "clouding" {
		args = map[string]any{"page": 1, "pageSize": 200}
	}
	data, err := executeProviderTool(ctx, provider, "list_images", args)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err = json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	rows := collectMaps(root["images"])
	if provider == "upcloud" {
		rows = collectMaps(nestedValue(root, "storages", "storage"))
	}
	out := []Image{}
	for _, row := range rows {
		id, name, disk := mapString(row, "id"), mapString(row, "name"), anyToInt(row["minimumSizeGb"])
		if provider == "upcloud" {
			id, name, disk = mapString(row, "uuid"), mapString(row, "title"), anyToInt(row["size"])
			if mapString(row, "type") != "template" || mapString(row, "access") != "public" || mapString(row, "state") != "online" {
				continue
			}
		} else {
			method := nestedMapString(row, "accessMethods", "sshKey")
			if (method != "optional" && method != "required") || nestedMapString(row, "accessMethods", "password") == "required" {
				continue
			}
		}
		lower := strings.ToLower(name)
		linux := false
		for _, flavor := range []string{"ubuntu", "debian", "rocky", "alma", "centos", "fedora", "arch linux"} {
			if strings.Contains(lower, flavor) {
				linux = true
			}
		}
		if !linux || strings.Contains(lower, "windows") || strings.Contains(lower, "arm") || strings.Contains(lower, "32 bit") || id == "" {
			continue
		}
		image := Image{Name: id, Description: name, OSFlavor: imageFlavor(name), OSVersion: imageVersion(name), Architecture: "x86", Platform: "linux", ResourceClass: "virtual", DiskSizeGB: disk}
		if provider == "upcloud" && mapString(row, "zone") != "" {
			image.AvailableIn = []string{mapString(row, "zone")}
		}
		out = append(out, image)
	}
	return out, nil
}

func validateCloudingUpcloudCreate(ctx *sdk.AppCtx, provider string, in *CreateInstanceInput, selected *ServerType) error {
	if selected == nil {
		return fmt.Errorf("%s size %s is absent from the live catalog", provider, in.Size)
	}
	if in.Platform != "" && in.Platform != "linux" {
		return errors.New("Clouding and UpCloud adapters require Linux")
	}
	if in.ResourceClass != "" && in.ResourceClass != "virtual" {
		return fmt.Errorf("%s adapter supports virtual Linux hosts", provider)
	}
	locations, err := cloudingUpcloudLocations(ctx, provider)
	if err != nil {
		return err
	}
	found := false
	for _, location := range locations {
		if location.Name == in.Region {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%s region %s is unavailable", provider, in.Region)
	}
	images, err := cloudingUpcloudImages(ctx, provider)
	if err != nil {
		return err
	}
	var image *Image
	for i := range images {
		if images[i].Name == in.Image && imageMatchesType(images[i], selected, in.Region) {
			image = &images[i]
			break
		}
	}
	if image == nil {
		return fmt.Errorf("%s image %s is not an eligible Linux SSH-key image", provider, in.Image)
	}
	size := selected.DiskGB
	if size < image.DiskSizeGB {
		size = image.DiskSizeGB
	}
	if size < 20 {
		size = 20
	}
	if boot := in.Storage.Boot; boot != nil {
		size = boot.SizeGB
	}
	if size < image.DiskSizeGB {
		return fmt.Errorf("boot disk requires at least %d GB", image.DiskSizeGB)
	}
	if provider == "clouding" {
		volumes, err := executeProviderTool(ctx, provider, "list_volume_sizes", map[string]any{"page": 1, "pageSize": 200})
		if err != nil {
			return err
		}
		var root map[string]any
		if err = json.Unmarshal(volumes, &root); err != nil {
			return err
		}
		allowed := false
		best := 0
		for _, row := range collectMaps(root["volumeSizes"]) {
			n := anyToInt(row["sizeGb"])
			if mapString(row, "storageType") != "ssd" {
				continue
			}
			if n == size {
				allowed = true
			}
			if n >= size && (best == 0 || n < best) {
				best = n
			}
		}
		if !allowed {
			if in.Storage.Boot != nil || best == 0 {
				return fmt.Errorf("Clouding boot size %d GB is absent from the live volume-size catalog", size)
			}
			size = best
		}
		if in.Firewall == nil {
			if _, err = cloudingFirewall(ctx); err != nil {
				return err
			}
		}
	} else if size > 4096 {
		return errors.New("UpCloud boot disk exceeds the supported 4096 GB limit")
	}
	if in.Storage.Boot == nil {
		in.Storage.Boot = &BootStorageRequest{SizeGB: size, StorageClass: "block", Tier: "balanced", DeletePolicy: "with_instance"}
	}
	if t := providerTypeForTier(provider, in.Storage.Boot.Tier, in.Storage.Boot.ProviderType); t != "ssd" && provider == "clouding" || t != "maxiops" && provider == "upcloud" {
		return fmt.Errorf("unsupported %s boot storage type %s", provider, t)
	}
	in.MonthlyCostCents = 0 // No complete, verified provider quote is available.
	return nil
}

func cloudingFirewall(ctx *sdk.AppCtx) (string, error) {
	bound, err := apiProviderBound(ctx, "clouding")
	if err != nil {
		return "", err
	}
	configured := ""
	config, err := sdk.GetConnectionPublicConfig(ctx.PlatformAPI(), bound.ConnectionID)
	if err != nil {
		return "", fmt.Errorf("read Clouding public connection config: %w", err)
	}
	if config != nil {
		configured = config.Fields["firewall_id"]
	}
	data, err := executePagedProviderTool(ctx, bound.ConnectionID, "clouding", "list_firewalls", map[string]any{"page": 1, "pageSize": 200})
	if err != nil {
		return "", err
	}
	var root map[string]any
	if err = json.Unmarshal(data, &root); err != nil {
		return "", err
	}
	for _, row := range collectMaps(root["values"]) {
		id := mapString(row, "id")
		if id == "" || configured != "" && id != configured {
			continue
		}
		for _, rule := range collectMaps(row["rules"]) {
			enabled, _ := rule["enabled"].(bool)
			protocol := strings.ToLower(mapString(rule, "protocol"))
			if enabled && (protocol == "tcp" || protocol == "all") && anyToInt(rule["portRangeMin"]) <= 22 && anyToInt(rule["portRangeMax"]) >= 22 && (configured != "" || mapString(rule, "sourceIp") == "0.0.0.0/0") {
				return id, nil
			}
		}
	}
	return "", errors.New("Clouding requires an existing public firewall allowing SSH; configure firewall_id on the provider connection")
}

type cloudingUpcloudMetadata struct {
	SSHKeyID  string `json:"clouding_ssh_key_id,omitempty"`
	BootTitle string `json:"upcloud_boot_title,omitempty"`
	BootID    string `json:"upcloud_boot_id,omitempty"`
}

func cloudingUpcloudMeta(raw string) cloudingUpcloudMetadata {
	var m cloudingUpcloudMetadata
	_ = json.Unmarshal([]byte(raw), &m)
	return m
}
func saveCloudingUpcloudMeta(ctx *sdk.AppCtx, inst *Instance, m cloudingUpcloudMetadata) error {
	data, _ := json.Marshal(m)
	inst.ProviderMetadataJSON = string(data)
	return dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"provider_metadata_json": string(data)})
}
func cloudingUpcloudCreateRequest(ctx *sdk.AppCtx, inst *Instance, in CreateInstanceInput, key string) (string, map[string]any, error) {
	if in.Storage.Boot == nil {
		return "", nil, errors.New("missing validated boot storage")
	}
	if in.Provider == "clouding" {
		var firewall string
		var err error
		if s, e := firewallLoad(ctx.AppDB(), inst.ID); e == nil && s.ResourceID != "" {
			firewall = s.ResourceID
		} else {
			firewall, err = cloudingFirewall(ctx)
		}
		if err != nil {
			return "", nil, err
		}
		data, err := executeProviderTool(ctx, "clouding", "create_ssh_key", map[string]any{"name": fmt.Sprintf("apteva-instance-%d-%s", inst.ID, newRequestID()), "publicKey": strings.TrimSpace(key)})
		if err != nil {
			return "", nil, err
		}
		id := jsonStringAt(data, "id")
		if id == "" {
			return "", nil, errors.New("Clouding key creation returned no id")
		}
		if err = saveCloudingUpcloudMeta(ctx, inst, cloudingUpcloudMetadata{SSHKeyID: id}); err != nil {
			cleanupErr := cloudingUpcloudDeleteTool(ctx, inst, "delete_ssh_key", map[string]any{"id": id})
			return "", nil, fmt.Errorf("persist Clouding SSH key %s: %w (cleanup: %v)", id, err, cleanupErr)
		}
		return "create_server", map[string]any{"name": in.Name, "hostname": providerHostname(in.Name), "flavorId": in.Size, "accessConfiguration": map[string]any{"sshKeyId": id, "savePassword": false}, "volume": map[string]any{"source": "image", "id": in.Image, "ssdGb": in.Storage.Boot.SizeGB}, "publicPortFirewallIds": []string{firewall}}, nil
	}
	m := cloudingUpcloudMetadata{BootTitle: "apteva-instances-" + newRequestID()}
	if err := saveCloudingUpcloudMeta(ctx, inst, m); err != nil {
		return "", nil, err
	}
	return "create_server", map[string]any{"server": map[string]any{
		"zone": in.Region, "plan": in.Size, "title": in.Name, "hostname": providerHostname(in.Name), "metadata": "yes",
		"login_user":      map[string]any{"username": "root", "create_password": "no", "ssh_keys": map[string]any{"ssh_key": []string{strings.TrimSpace(key)}}},
		"storage_devices": map[string]any{"storage_device": []map[string]any{{"action": "clone", "storage": in.Image, "title": m.BootTitle, "size": in.Storage.Boot.SizeGB, "tier": "maxiops", "type": "disk"}}},
		"networking":      map[string]any{"interfaces": map[string]any{"interface": []map[string]any{{"type": "public", "ip_addresses": map[string]any{"ip_address": []map[string]any{{"family": "IPv4"}}}}}}},
	}}, nil
}
func providerHostname(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	result := strings.Trim(b.String(), "-")
	if len(result) > 63 {
		result = strings.TrimRight(result[:63], "-")
	}
	if result == "" {
		return "apteva-host"
	}
	return result
}
func parseCloudingUpcloudResource(provider string, data json.RawMessage) (string, string, string) {
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return "", "", ""
	}
	if server, ok := root["server"].(map[string]any); ok {
		root = server
	}
	if provider == "clouding" {
		if mapValue(root, "flavor") == nil || mapValue(root, "status") == nil {
			return "", "", ""
		}
		return mapString(root, "id"), usableProviderIP(mapString(root, "publicIp"), 4), ""
	}
	if mapValue(root, "zone") == nil || mapValue(root, "state") == nil {
		return "", "", ""
	}
	ipv4, ipv6 := "", ""
	for _, address := range collectMaps(nestedValue(root, "ip_addresses", "ip_address")) {
		if mapString(address, "access") != "public" {
			continue
		}
		if a := usableProviderIP(mapString(address, "address"), 4); a != "" {
			ipv4 = a
		}
		if a := usableProviderIP(mapString(address, "address"), 6); a != "" {
			ipv6 = a
		}
	}
	return mapString(root, "uuid"), ipv4, ipv6
}
func reconcileCloudingUpcloudBoot(ctx *sdk.AppCtx, inst *Instance) error {
	if inst.Provider != "upcloud" {
		return nil
	}
	m := cloudingUpcloudMeta(inst.ProviderMetadataJSON)
	if m.BootID != "" {
		return nil
	}
	if m.BootTitle == "" {
		return errors.New("UpCloud owned boot-storage metadata is missing")
	}
	data, err := executeProviderToolOnConnection(ctx, inst.ProviderConnectionID, "upcloud", "get_server", map[string]any{"server_id": inst.ProviderID})
	if err != nil {
		return err
	}
	m.BootID = upcloudOwnedBoot(data, m.BootTitle)
	if m.BootID == "" {
		return errors.New("UpCloud did not report the deployment-owned boot disk; retry recovery after cloning finishes")
	}
	return saveCloudingUpcloudMeta(ctx, inst, m)
}
func upcloudOwnedBoot(data json.RawMessage, title string) string {
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return ""
	}
	for _, disk := range collectMaps(nestedValue(root, "server", "storage_devices", "storage_device")) {
		if mapString(disk, "storage_title") == title && mapString(disk, "type") == "disk" {
			return mapString(disk, "storage")
		}
	}
	return ""
}

// Deletion never infers ownership from a human name, and never requests the
// provider's blanket delete-all-attached-disks option.
func cloudingUpcloudDestroy(ctx *sdk.AppCtx, inst *Instance) error {
	if inst.ProviderID == "" {
		meta := cloudingUpcloudMeta(inst.ProviderMetadataJSON)
		if meta.SSHKeyID != "" || meta.BootTitle != "" || inst.CreatePending {
			return errors.New("provider creation outcome is unknown; retain inventory and reconcile in the provider account")
		}
		return nil
	} // A lost create response is unknown, not safe to retry.
	bound, err := storageBinding(ctx, inst.Provider, inst.ProviderConnectionID)
	if err != nil {
		return err
	}
	instCopy := *inst
	instCopy.ProviderConnectionID = bound.ConnectionID
	inst = &instCopy
	m := cloudingUpcloudMeta(inst.ProviderMetadataJSON)
	if inst.Provider == "upcloud" {
		deadline := time.Now().Add(5 * time.Minute)
		for {
			res, err := ctx.PlatformAPI().ExecuteIntegrationTool(inst.ProviderConnectionID, "get_server", map[string]any{"server_id": inst.ProviderID})
			if err != nil {
				return err
			}
			if upstreamStatus(res) == 404 {
				if m.BootID == "" && m.BootTitle != "" {
					return errors.New("UpCloud server is absent but owned boot disk is unknown; inventory retained for recovery")
				}
				break
			}
			if res == nil || !res.Success {
				return fmt.Errorf("UpCloud inspect before deletion: %s", upstreamErrorString(res))
			}
			if m.BootID == "" && m.BootTitle != "" {
				m.BootID = upcloudOwnedBoot(res.Data, m.BootTitle)
				if m.BootID != "" {
					if err = saveCloudingUpcloudMeta(ctx, inst, m); err != nil {
						return err
					}
				}
			}
			var root map[string]any
			if err = json.Unmarshal(res.Data, &root); err != nil {
				return err
			}
			state := nestedMapString(root, "server", "state")
			if state == "stopped" {
				if m.BootID == "" {
					return errors.New("UpCloud owned boot disk is unknown; inventory retained for recovery")
				}
				break
			}
			if state == "started" {
				if _, err = executeProviderToolOnConnection(ctx, inst.ProviderConnectionID, "upcloud", "stop_server", map[string]any{"server_id": inst.ProviderID, "stop_server": map[string]any{"stop_type": "soft", "timeout": 60, "timeout_action": "ignore"}}); err != nil {
					return err
				}
			} else if state != "maintenance" {
				return fmt.Errorf("UpCloud cannot confirm shutdown in state %q", state)
			}
			if time.Now().After(deadline) {
				return errors.New("UpCloud shutdown not confirmed; deletion deferred")
			}
			if err = sleepContext(instanceWorkerContext(ctx, inst.ID), 2*time.Second); err != nil {
				return err
			}
		}
	}
	key, args := "id", map[string]any{}
	if inst.Provider == "upcloud" {
		key = "server_id"
		args["storages"] = 0
	}
	args[key] = inst.ProviderID
	if err := cloudingUpcloudDeleteTool(ctx, inst, "delete_server", args); err != nil {
		return err
	}
	// Clouding deletes asynchronously; wait for absence before removing the key
	// or removing inventory. UpCloud normally returns 204 synchronously.
	deadline := time.Now().Add(5 * time.Minute)
	for {
		tool, getArgs, _ := apiProviderGetRequest(inst)
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(inst.ProviderConnectionID, tool, getArgs)
		if err != nil {
			return err
		}
		if upstreamStatus(res) == 404 {
			break
		}
		if res == nil || !res.Success {
			return fmt.Errorf("%s deletion verification failed: %s", inst.Provider, upstreamErrorString(res))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s deletion still pending; retry to reconcile", inst.Provider)
		}
		if err = sleepContext(instanceWorkerContext(ctx, inst.ID), 2*time.Second); err != nil {
			return err
		}
	}
	if inst.Provider == "clouding" && m.SSHKeyID != "" {
		data, err := ctx.PlatformAPI().ExecuteIntegrationTool(inst.ProviderConnectionID, "get_ssh_key", map[string]any{"id": m.SSHKeyID})
		if err != nil {
			return err
		}
		if upstreamStatus(data) != 404 {
			if data == nil || !data.Success {
				return fmt.Errorf("Clouding SSH key ownership check failed: %s", upstreamErrorString(data))
			}
			if strings.TrimSpace(jsonStringAt(data.Data, "publicKey")) != strings.TrimSpace(inst.SSHPublicKey) {
				return errors.New("Clouding SSH key ownership mismatch")
			}
			if err = cloudingUpcloudDeleteTool(ctx, inst, "delete_ssh_key", map[string]any{"id": m.SSHKeyID}); err != nil {
				return err
			}
		}
	}
	if inst.Provider == "upcloud" && m.BootID != "" {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(inst.ProviderConnectionID, "get_volume", map[string]any{"volume_id": m.BootID})
		if err != nil {
			return err
		}
		if upstreamStatus(res) != 404 {
			if res == nil || !res.Success {
				return errors.New("UpCloud boot disk ownership check failed")
			}
			var root map[string]any
			if err = json.Unmarshal(res.Data, &root); err != nil {
				return err
			}
			if nestedMapString(root, "storage", "title") != m.BootTitle {
				return errors.New("UpCloud boot disk ownership mismatch")
			}
			if err = cloudingUpcloudDeleteTool(ctx, inst, "delete_volume", map[string]any{"volume_id": m.BootID}); err != nil {
				return err
			}
		}
	}
	return nil
}
func cloudingUpcloudDeleteTool(ctx *sdk.AppCtx, inst *Instance, tool string, args map[string]any) error {
	// Resolve the exact original connection before every lifecycle operation.
	bound, err := storageBinding(ctx, inst.Provider, inst.ProviderConnectionID)
	if err != nil {
		return err
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, args)
	if err != nil {
		return err
	}
	if res != nil && (res.Success || upstreamStatus(res) == 404) {
		return nil
	}
	return fmt.Errorf("%s.%s failed: %s", inst.Provider, tool, upstreamErrorString(res))
}

func boolPtr(value bool) *bool { return &value }
