package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const scalewayApplePrefix = "apple-silicon/"

var scalewayAppleZones = []string{"fr-par-1", "fr-par-3"}

type scalewayAppleProviderMetadata struct {
	SSHKeyID  string `json:"ssh_key_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
}

func isScalewayAppleSize(size string) bool {
	return strings.HasPrefix(size, scalewayApplePrefix)
}

func scalewayAppleID(value string) string {
	return strings.TrimPrefix(value, scalewayApplePrefix)
}

func isScalewayAppleInstance(inst *Instance) bool {
	return inst != nil && normalizeProvider(inst.Provider) == "scaleway" &&
		(isScalewayAppleSize(inst.Size) || (inst.ResourceClass == "bare_metal" && inst.Platform == "macos"))
}

func parseScalewayAppleProducts(data json.RawMessage) ([]ServerType, error) {
	var response struct {
		Products []struct {
			Product     string `json:"product"`
			Description string `json:"description"`
			Status      string `json:"status"`
			Locality    struct {
				Zone string `json:"zone"`
			} `json:"locality"`
			Price struct {
				Retail struct {
					Currency string `json:"currency_code"`
					Units    int64  `json:"units"`
					Nanos    int64  `json:"nanos"`
				} `json:"retail_price"`
			} `json:"price"`
			Unit struct {
				Name string `json:"unit"`
			} `json:"unit_of_measure"`
			Properties struct {
				Apple struct {
					ServerType string `json:"server_type"`
				} `json:"apple_silicon"`
				Hardware struct {
					CPU struct {
						Description string `json:"description"`
						Type        string `json:"type"`
						Physical    struct {
							Sockets        int `json:"sockets"`
							CoresPerSocket int `json:"cores_per_socket"`
						} `json:"physical"`
					} `json:"cpu"`
					RAM struct {
						Size int64 `json:"size"`
					} `json:"ram"`
					Storage struct {
						Total int64 `json:"total"`
					} `json:"storage"`
					GPU struct {
						Count int    `json:"count"`
						Type  string `json:"type"`
					} `json:"gpu"`
				} `json:"hardware"`
			} `json:"properties"`
		} `json:"products"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode Scaleway Apple silicon products: %w", err)
	}

	byName := make(map[string]*ServerType)
	for _, product := range response.Products {
		if product.Status != "" && product.Status != "general_availability" && product.Status != "public_beta" {
			continue
		}
		providerType := product.Properties.Apple.ServerType
		if providerType == "" {
			continue
		}
		name := scalewayApplePrefix + providerType
		row := byName[name]
		if row == nil {
			platform := "macos"
			if strings.Contains(strings.ToLower(providerType), "asahi") {
				platform = "linux"
			}
			row = &ServerType{
				Name: name, Description: firstNonEmpty(product.Product, providerType),
				CPUType: "dedicated", Architecture: "arm64", Platform: platform,
				ResourceClass: "bare_metal",
			}
			byName[name] = row
		}
		if product.Locality.Zone != "" && !containsString(row.AvailableIn, product.Locality.Zone) {
			row.AvailableIn = append(row.AvailableIn, product.Locality.Zone)
		}
		hardware := product.Properties.Hardware
		cores := hardware.CPU.Physical.Sockets * hardware.CPU.Physical.CoresPerSocket
		if cores > 0 {
			row.Cores = cores
		}
		if hardware.RAM.Size > 0 {
			row.MemoryGB = bytesToGiB(hardware.RAM.Size)
		}
		if hardware.Storage.Total > 0 {
			row.DiskGB = int(float64(hardware.Storage.Total) / 1_000_000_000)
		}
		if hardware.GPU.Type != "" {
			model := hardware.GPU.Type
			if hardware.GPU.Count > 0 {
				model = fmt.Sprintf("%s (%d-core GPU)", model, hardware.GPU.Count)
			}
			row.Accelerators = []AcceleratorDef{{Kind: "gpu", Vendor: "apple", Model: model, Count: 1}}
		}
		price := float64(product.Price.Retail.Units) + float64(product.Price.Retail.Nanos)/1e9
		if strings.EqualFold(product.Price.Retail.Currency, "EUR") {
			switch strings.ToLower(product.Unit.Name) {
			case "month":
				row.MonthlyPriceEUR = price
			case "hour":
				row.HourlyPriceEUR = price
			}
		}
	}

	out := make([]ServerType, 0, len(byName))
	for _, row := range byName {
		sort.Strings(row.AvailableIn)
		out = append(out, *row)
	}
	return out, nil
}

func parseScalewayAppleImages(data json.RawMessage, zone string) ([]Image, error) {
	var response struct {
		OS []struct {
			ID         string   `json:"id"`
			Name       string   `json:"name"`
			Label      string   `json:"label"`
			Family     string   `json:"family"`
			Version    string   `json:"version"`
			Xcode      string   `json:"xcode_version"`
			IsBeta     bool     `json:"is_beta"`
			Compatible []string `json:"compatible_server_types"`
			Supported  []struct {
				ServerType string `json:"server_type"`
			} `json:"supported_server_types"`
		} `json:"os"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode Scaleway Apple silicon images: %w", err)
	}
	out := make([]Image, 0, len(response.OS))
	for _, os := range response.OS {
		if os.ID == "" || os.IsBeta {
			continue
		}
		text := strings.ToLower(os.Family + " " + os.Name + " " + os.Label)
		platform, flavor := "macos", "macos"
		if strings.Contains(text, "asahi") || strings.Contains(text, "linux") {
			platform, flavor = "linux", "asahi"
		}
		compatible := os.Compatible
		for _, supported := range os.Supported {
			if supported.ServerType != "" && !containsString(compatible, supported.ServerType) {
				compatible = append(compatible, supported.ServerType)
			}
		}
		for i := range compatible {
			compatible[i] = scalewayApplePrefix + scalewayAppleID(compatible[i])
		}
		description := firstNonEmpty(os.Label, os.Name, os.ID)
		if os.Xcode != "" {
			description += " / Xcode " + os.Xcode
		}
		out = append(out, Image{
			Name: scalewayApplePrefix + os.ID, Description: description,
			OSFlavor: flavor, OSVersion: firstNonEmpty(os.Version, os.Family),
			Architecture: "arm64", Platform: platform, ResourceClass: "bare_metal",
			AvailableIn: []string{zone}, CompatibleTypes: compatible,
		})
	}
	return out, nil
}

func mergeCatalogImages(images []Image) []Image {
	byName := make(map[string]int, len(images))
	out := make([]Image, 0, len(images))
	for _, image := range images {
		if at, ok := byName[image.Name]; ok {
			for _, zone := range image.AvailableIn {
				if !containsString(out[at].AvailableIn, zone) {
					out[at].AvailableIn = append(out[at].AvailableIn, zone)
				}
			}
			continue
		}
		byName[image.Name] = len(out)
		out = append(out, image)
	}
	return out
}

func scalewayAppleResources(serverType ServerType) string {
	resources := InstanceResources{
		CPU: &CPUResource{Cores: float64(serverType.Cores)}, MemoryGB: serverType.MemoryGB,
		DiskGB: serverType.DiskGB, Accelerators: serverType.Accelerators,
	}
	return marshalJSONString(resources, "{}")
}

func registerScalewayAppleSSHKey(ctx *sdk.AppCtx, instanceID int64, name, publicKey string) (scalewayAppleProviderMetadata, error) {
	projectID, err := scalewayDefaultProject(ctx)
	if err != nil {
		return scalewayAppleProviderMetadata{}, err
	}
	data, err := executeProviderTool(ctx, "scaleway", "ssh_key_create", map[string]any{
		"name":       fmt.Sprintf("apteva-instance-%d-%s", instanceID, name),
		"public_key": publicKey,
		"project_id": projectID,
	})
	if err != nil {
		return scalewayAppleProviderMetadata{}, fmt.Errorf("register Mac SSH key: %w", err)
	}
	keyID := jsonStringAt(data, "id")
	if keyID == "" {
		return scalewayAppleProviderMetadata{}, errors.New("Scaleway SSH key response missing id")
	}
	return scalewayAppleProviderMetadata{SSHKeyID: keyID, ProjectID: projectID}, nil
}

func scalewayAppleMetadataJSON(metadata scalewayAppleProviderMetadata) string {
	return marshalJSONString(metadata, "{}")
}

func parseScalewayAppleMetadata(raw string) scalewayAppleProviderMetadata {
	var metadata scalewayAppleProviderMetadata
	_ = json.Unmarshal([]byte(raw), &metadata)
	return metadata
}

func scalewayAppleResponseFields(data json.RawMessage) map[string]any {
	fields := map[string]any{}
	if user := jsonStringAt(data, "ssh_username"); user != "" {
		fields["ssh_user"] = user
	}
	if deletableAt := jsonStringAt(data, "deletable_at"); deletableAt != "" {
		fields["deletable_at"] = deletableAt
	}
	return fields
}

func scalewayAppleDestroy(ctx *sdk.AppCtx, inst *Instance) error {
	bound, err := apiProviderBound(ctx, "scaleway")
	if err != nil {
		return err
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "apple_server_delete", map[string]any{
		"zone": inst.Region, "server_id": inst.ProviderID,
	})
	if err != nil {
		return fmt.Errorf("scaleway.apple_server_delete: %w", err)
	}
	if res == nil || !res.Success {
		if status := upstreamStatus(res); status != 404 && status != 410 {
			return fmt.Errorf("scaleway.apple_server_delete returned: %s", upstreamErrorString(res))
		}
	}
	metadata := parseScalewayAppleMetadata(inst.ProviderMetadataJSON)
	return deleteScalewayAppleSSHKeyWithBinding(ctx, bound, metadata.SSHKeyID)
}

func deleteScalewayAppleSSHKey(ctx *sdk.AppCtx, keyID string) error {
	if keyID == "" {
		return nil
	}
	bound, err := apiProviderBound(ctx, "scaleway")
	if err != nil {
		return err
	}
	return deleteScalewayAppleSSHKeyWithBinding(ctx, bound, keyID)
}

func deleteScalewayAppleSSHKeyWithBinding(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, keyID string) error {
	if keyID == "" {
		return nil
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "ssh_key_delete", map[string]any{"ssh_key_id": keyID})
	if err != nil {
		return fmt.Errorf("scaleway.ssh_key_delete: %w", err)
	}
	if res == nil || !res.Success {
		if status := upstreamStatus(res); status != 404 && status != 410 {
			return fmt.Errorf("scaleway.ssh_key_delete returned: %s", upstreamErrorString(res))
		}
	}
	return nil
}

func scalewayAppleCanDelete(inst *Instance, now time.Time) bool {
	if !isScalewayAppleInstance(inst) || inst.DeletableAt == "" {
		return true
	}
	deletableAt, err := time.Parse(time.RFC3339Nano, inst.DeletableAt)
	return err != nil || !now.Before(deletableAt)
}

func bytesToGiB(value int64) float64 {
	return float64(value) / (1024 * 1024 * 1024)
}
