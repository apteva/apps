package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const scalewayElasticMetalPrefix = "elastic-metal/"

var scalewayElasticMetalZones = []string{"fr-par-1", "fr-par-2", "nl-ams-1", "nl-ams-2", "pl-waw-2", "pl-waw-3"}

type ElasticMetalConfig struct {
	RAIDLevel          string         `json:"raid_level,omitempty"`
	PartitioningSchema map[string]any `json:"partitioning_schema,omitempty"`
	RetainFlexibleIPs  bool           `json:"retain_flexible_ips,omitempty"`
}

type scalewayElasticMetalMetadata struct {
	SSHKeyID  string `json:"ssh_key_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
}

func isScalewayElasticMetalSize(value string) bool {
	return strings.HasPrefix(value, scalewayElasticMetalPrefix)
}
func isScalewayElasticMetalInstance(inst *Instance) bool {
	return inst != nil && inst.Provider == "scaleway" && isScalewayElasticMetalSize(inst.Size)
}
func scalewayElasticMetalID(value string) string {
	return strings.TrimPrefix(value, scalewayElasticMetalPrefix)
}

func scalewayElasticMetalRAIDLevel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "raid_level_")
	value = strings.TrimPrefix(value, "raid")
	if value == "" {
		return ""
	}
	return "raid_level_" + value
}

func scalewayElasticMetalListServerTypes(ctx *sdk.AppCtx) ([]ServerType, error) {
	byName := map[string]*ServerType{}
	var lastErr error
	for _, zone := range scalewayElasticMetalZones {
		data, err := executeProviderTool(ctx, "scaleway", "elastic_metal_offers_list", map[string]any{"zone": zone, "page_size": 100})
		if err != nil {
			lastErr = err
			continue
		}
		rows, err := parseScalewayElasticMetalOffers(data, zone)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if existing := byName[row.Name]; existing != nil {
				if !containsString(existing.AvailableIn, zone) {
					existing.AvailableIn = append(existing.AvailableIn, zone)
				}
				continue
			}
			copy := row
			byName[row.Name] = &copy
		}
	}
	if len(byName) == 0 && lastErr != nil {
		return nil, lastErr
	}
	out := make([]ServerType, 0, len(byName))
	for _, row := range byName {
		sort.Strings(row.AvailableIn)
		out = append(out, *row)
	}
	return out, nil
}

func parseScalewayElasticMetalOffers(data json.RawMessage, zone string) ([]ServerType, error) {
	var response struct {
		Offers []struct {
			ID                 string          `json:"id"`
			Name               string          `json:"name"`
			Stock              string          `json:"stock"`
			SubscriptionPeriod string          `json:"subscription_period"`
			PricePerHour       json.RawMessage `json:"price_per_hour"`
			PricePerMonth      json.RawMessage `json:"price_per_month"`
			CPUs               []struct {
				Name        string `json:"name"`
				CoreCount   int    `json:"core_count"`
				ThreadCount int    `json:"thread_count"`
			} `json:"cpus"`
			Memories []struct {
				Capacity int64  `json:"capacity"`
				Type     string `json:"type"`
			} `json:"memories"`
			Disks []struct {
				Capacity int64  `json:"capacity"`
				Type     string `json:"type"`
			} `json:"disks"`
			IncompatibleOSIDs []string `json:"incompatible_os_ids"`
		} `json:"offers"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode Scaleway Elastic Metal offers: %w", err)
	}
	out := make([]ServerType, 0, len(response.Offers))
	for _, offer := range response.Offers {
		if offer.ID == "" || strings.EqualFold(offer.Stock, "empty") {
			continue
		}
		cores, threads := 0, 0
		var memory, disk int64
		diskParts := []string{}
		for _, cpu := range offer.CPUs {
			cores += cpu.CoreCount
			threads += cpu.ThreadCount
		}
		for _, ram := range offer.Memories {
			memory += ram.Capacity
		}
		for _, drive := range offer.Disks {
			disk += drive.Capacity
			diskParts = append(diskParts, fmt.Sprintf("%.0f GB %s", bytesToDecimalGB(drive.Capacity), strings.ToUpper(drive.Type)))
		}
		description := firstNonEmpty(offer.Name, offer.ID)
		if len(diskParts) > 0 {
			description += " — " + strings.Join(diskParts, " + ")
		}
		if threads > 0 {
			description += fmt.Sprintf(" — %dc/%dt", cores, threads)
		}
		row := ServerType{Name: scalewayElasticMetalPrefix + offer.ID, Description: description, Cores: cores, MemoryGB: bytesToGiB(memory), DiskGB: int(bytesToDecimalGB(disk)), CPUType: "dedicated", Architecture: "x86", Platform: "linux", ResourceClass: "bare_metal", AvailableIn: []string{zone}, HourlyPriceEUR: flexiblePrice(offer.PricePerHour), MonthlyPriceEUR: flexiblePrice(offer.PricePerMonth)}
		for _, osID := range offer.IncompatibleOSIDs {
			row.IncompatibleImages = append(row.IncompatibleImages, scalewayElasticMetalPrefix+osID)
		}
		row.BootStorage = []StorageConstraint{{StorageClass: "local", ProviderType: "local_disk", MaxSizeGB: row.DiskGB, Technology: strings.ToLower(strings.Join(diskParts, " ")), Persistent: false, Replication: "RAID configurable", Billing: "included"}}
		out = append(out, row)
	}
	return out, nil
}

func scalewayElasticMetalImages(ctx *sdk.AppCtx) ([]Image, error) {
	out := []Image{}
	var lastErr error
	for _, zone := range scalewayElasticMetalZones {
		data, err := executeProviderTool(ctx, "scaleway", "elastic_metal_os_list", map[string]any{"zone": zone, "page_size": 100})
		if err != nil {
			lastErr = err
			continue
		}
		var response struct {
			OS []struct {
				ID           string   `json:"id"`
				Name         string   `json:"name"`
				Label        string   `json:"label"`
				Version      string   `json:"version"`
				Family       string   `json:"family"`
				Architecture string   `json:"architecture"`
				Enabled      bool     `json:"enabled"`
				Allowed      bool     `json:"allowed"`
				CloudInit    bool     `json:"cloud_init_supported"`
				Compatible   []string `json:"compatible_offer_ids"`
			} `json:"os"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, err
		}
		for _, os := range response.OS {
			if os.ID == "" || !os.Enabled || !os.Allowed || !os.CloudInit {
				continue
			}
			compatible := make([]string, 0, len(os.Compatible))
			for _, id := range os.Compatible {
				compatible = append(compatible, scalewayElasticMetalPrefix+id)
			}
			description := firstNonEmpty(os.Label, os.Name, os.ID)
			if os.Version != "" && !strings.Contains(description, os.Version) {
				description += " " + os.Version
			}
			out = append(out, Image{Name: scalewayElasticMetalPrefix + os.ID, Description: description, OSFlavor: strings.ToLower(firstNonEmpty(os.Family, os.Name)), OSVersion: os.Version, Architecture: firstNonEmpty(os.Architecture, "x86"), Platform: "linux", ResourceClass: "bare_metal", AvailableIn: []string{zone}, CompatibleTypes: compatible})
		}
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return mergeCatalogImages(out), nil
}

func scalewayElasticMetalProvision(ctx *sdk.AppCtx, in CreateInstanceInput) (*Instance, error) {
	priv, pub, err := generateSSHKeypair()
	if err != nil {
		return nil, err
	}
	in.Provider, in.Status, in.SSHPrivateKey, in.SSHPublicKey = "scaleway", "provisioning", priv, pub
	in.SSHUser, in.Platform, in.ResourceClass = "root", "linux", "bare_metal"
	if in.MonthlyCostCents == 0 {
		in.MonthlyCostCents = apiProviderMonthlyCostCents(ctx, "scaleway", in.Size)
	}
	inst, err := dbCreateInstance(ctx.AppDB(), in)
	if err != nil {
		return nil, err
	}
	emitInstanceCreated(ctx, inst)
	emitInstanceStatus(ctx, inst)
	_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"lifecycle_stage": "ProviderCreate"})
	access, err := registerScalewaySSHKeyOnConnection(ctx, in.ProviderConnectionID, inst.ID, in.Name, pub)
	if err != nil {
		failInstanceStage(ctx, inst.ID, "ProviderCreate", err)
		return nil, err
	}
	metadata := scalewayElasticMetalMetadata{SSHKeyID: access.SSHKeyID, ProjectID: access.ProjectID}
	metaJSON, _ := json.Marshal(metadata)
	_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"provider_metadata_json": string(metaJSON)})
	install := map[string]any{"os_id": scalewayElasticMetalID(in.Image), "hostname": scalewayDediboxHostname(in.Name), "ssh_key_ids": []string{access.SSHKeyID}, "user": "root"}
	var schema map[string]any
	if in.ElasticMetal != nil {
		schema = in.ElasticMetal.PartitioningSchema
	}
	if in.ElasticMetal != nil && in.ElasticMetal.RAIDLevel != "" && len(schema) == 0 {
		defaultData, defaultErr := executeProviderToolOnConnection(ctx, in.ProviderConnectionID, "scaleway", "elastic_metal_partitioning_default_get", map[string]any{"zone": in.Region, "offer_id": scalewayElasticMetalID(in.Size), "os_id": scalewayElasticMetalID(in.Image)})
		if defaultErr != nil {
			_ = deleteScalewaySSHKeyOnConnection(ctx, in.ProviderConnectionID, access.SSHKeyID)
			failInstanceStage(ctx, inst.ID, "ProviderCreate", defaultErr)
			return nil, defaultErr
		}
		var root map[string]any
		if err := json.Unmarshal(defaultData, &root); err != nil {
			_ = deleteScalewaySSHKeyOnConnection(ctx, in.ProviderConnectionID, access.SSHKeyID)
			failInstanceStage(ctx, inst.ID, "ProviderCreate", err)
			return nil, err
		}
		if nested, ok := root["partitioning_schema"].(map[string]any); ok {
			schema = nested
		} else {
			schema = root
		}
		raids, _ := schema["raids"].([]any)
		if len(raids) == 0 {
			err = fmt.Errorf("Scaleway default partitioning schema has no RAID set to configure")
			_ = deleteScalewaySSHKeyOnConnection(ctx, in.ProviderConnectionID, access.SSHKeyID)
			failInstanceStage(ctx, inst.ID, "ProviderCreate", err)
			return nil, err
		}
		for _, item := range raids {
			if raid, ok := item.(map[string]any); ok {
				raid["level"] = scalewayElasticMetalRAIDLevel(in.ElasticMetal.RAIDLevel)
			}
		}
	}
	if len(schema) > 0 {
		if _, validateErr := executeProviderToolOnConnection(ctx, in.ProviderConnectionID, "scaleway", "elastic_metal_partitioning_validate", map[string]any{"zone": in.Region, "offer_id": scalewayElasticMetalID(in.Size), "os_id": scalewayElasticMetalID(in.Image), "partitioning_schema": schema}); validateErr != nil {
			_ = deleteScalewaySSHKeyOnConnection(ctx, in.ProviderConnectionID, access.SSHKeyID)
			failInstanceStage(ctx, inst.ID, "ProviderCreate", validateErr)
			return nil, validateErr
		}
		install["partitioning_schema"] = schema
	}
	args := map[string]any{"zone": in.Region, "offer_id": scalewayElasticMetalID(in.Size), "project_id": access.ProjectID, "name": in.Name, "description": "Managed by Apteva Instances", "tags": []string{"managed-by-apteva", "apteva-instance-" + fmt.Sprint(inst.ID)}, "install": install, "user_data": map[string]any{"value": base64.StdEncoding.EncodeToString([]byte(buildCloudInit(pub)))}}
	data, err := executeProviderToolOnConnection(ctx, in.ProviderConnectionID, "scaleway", "elastic_metal_server_create", args)
	if err != nil {
		_ = deleteScalewaySSHKeyOnConnection(ctx, in.ProviderConnectionID, access.SSHKeyID)
		failInstanceStage(ctx, inst.ID, "ProviderCreate", err)
		return nil, err
	}
	providerID := findJSONScalar(data, "id")
	if providerID == "" {
		err = fmt.Errorf("Scaleway Elastic Metal create response missing server id")
		_ = deleteScalewaySSHKeyOnConnection(ctx, in.ProviderConnectionID, access.SSHKeyID)
		failInstanceStage(ctx, inst.ID, "ProviderCreate", err)
		return nil, err
	}
	if err = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"provider_id": providerID, "provider_metadata_json": string(metaJSON), "lifecycle_stage": "Boot"}); err != nil {
		return nil, err
	}
	kickAPIProviderReadinessProbe(ctx, inst.ID)
	return dbGetInstance(ctx.AppDB(), inst.ID)
}

func scalewayElasticMetalDestroy(ctx *sdk.AppCtx, inst *Instance) error {
	return scalewayElasticMetalDestroyWithOptions(ctx, inst, false)
}

func scalewayElasticMetalDestroyWithOptions(ctx *sdk.AppCtx, inst *Instance, retainFlexibleIPs bool) error {
	metadata := scalewayElasticMetalMetadata{}
	_ = json.Unmarshal([]byte(inst.ProviderMetadataJSON), &metadata)
	if !retainFlexibleIPs && inst.ProviderID != "" {
		data, listErr := executeProviderToolOnConnection(ctx, inst.ProviderConnectionID, "scaleway", "elastic_metal_flexible_ips_list", map[string]any{"zone": inst.Region, "project_id": metadata.ProjectID, "page_size": 100})
		if listErr != nil {
			return fmt.Errorf("list Elastic Metal Flexible IPs before deletion: %w", listErr)
		}
		var root any
		_ = json.Unmarshal(data, &root)
		for _, object := range collectMaps(root) {
			fipID := mapString(object, "id")
			if fipID == "" {
				continue
			}
			serverID := firstNonEmpty(mapString(object, "server_id"), nestedMapString(object, "server", "id"))
			if serverID != inst.ProviderID {
				continue
			}
			if _, deleteErr := executeProviderToolOnConnection(ctx, inst.ProviderConnectionID, "scaleway", "elastic_metal_flexible_ip_delete", map[string]any{"zone": inst.Region, "fip_id": fipID}); deleteErr != nil && !strings.Contains(deleteErr.Error(), "status=404") {
				return deleteErr
			}
		}
	}
	if inst.ProviderID != "" {
		_, err := executeProviderToolOnConnection(ctx, inst.ProviderConnectionID, "scaleway", "elastic_metal_server_delete", map[string]any{"zone": inst.Region, "server_id": inst.ProviderID})
		if err != nil && !strings.Contains(err.Error(), "status=404") {
			return err
		}
	}
	return deleteScalewaySSHKeyOnConnection(ctx, inst.ProviderConnectionID, metadata.SSHKeyID)
}
