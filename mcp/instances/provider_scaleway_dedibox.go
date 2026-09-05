package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	sdk "github.com/apteva/app-sdk"
)

const scalewayDediboxPrefix = "dedibox/"

var (
	scalewayDediboxZones           = []string{"fr-par-1", "fr-par-2", "nl-ams-1"}
	scalewayDediboxPollInterval    = 30 * time.Second
	scalewayDediboxDeliveryTimeout = 2 * time.Hour
	scalewayDediboxInstallTimeout  = 90 * time.Minute
)

type scalewayDediboxProviderMetadata struct {
	ServiceID      string `json:"service_id,omitempty"`
	SSHKeyID       string `json:"ssh_key_id,omitempty"`
	ProjectID      string `json:"project_id,omitempty"`
	DesiredImage   string `json:"desired_image,omitempty"`
	InstallStarted bool   `json:"install_started,omitempty"`
}

func isScalewayDediboxSize(size string) bool {
	return strings.HasPrefix(size, scalewayDediboxPrefix)
}

func isScalewayDediboxInstance(inst *Instance) bool {
	return inst != nil && normalizeProvider(inst.Provider) == "scaleway" && isScalewayDediboxSize(inst.Size)
}

func scalewayDediboxOfferID(size string) (int64, error) {
	value := strings.TrimPrefix(size, scalewayDediboxPrefix)
	if at := strings.IndexByte(value, '/'); at >= 0 {
		value = value[:at]
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid Scaleway Dedibox size %q; choose a live dedibox/<offer-id> catalog entry", size)
	}
	return id, nil
}

func scalewayDediboxImages() []Image {
	images := []struct {
		name, description, flavor, version string
	}{
		{"ubuntu-24.04", "Ubuntu 24.04 LTS", "ubuntu", "24.04"},
		{"ubuntu-22.04", "Ubuntu 22.04 LTS", "ubuntu", "22.04"},
		{"debian-12", "Debian 12", "debian", "12"},
	}
	out := make([]Image, 0, len(images))
	for _, image := range images {
		out = append(out, Image{
			Name: scalewayDediboxPrefix + image.name, Description: image.description,
			OSFlavor: image.flavor, OSVersion: image.version, Architecture: "x86",
			Platform: "linux", ResourceClass: "bare_metal", AvailableIn: append([]string(nil), scalewayDediboxZones...),
		})
	}
	return out
}

func scalewayDediboxListServerTypes(ctx *sdk.AppCtx) ([]ServerType, error) {
	projectID, err := scalewayDefaultProject(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]*ServerType{}
	var lastErr error
	for _, zone := range scalewayDediboxZones {
		data, callErr := executeProviderTool(ctx, "scaleway", "dedibox_offers_list", map[string]any{
			"zone": zone, "project_id": projectID, "page_size": 100, "order_by": "price_asc", "available_only": true,
		})
		if callErr != nil {
			lastErr = callErr
			continue
		}
		rows, parseErr := parseScalewayDediboxOffers(data, zone)
		if parseErr != nil {
			return nil, parseErr
		}
		for _, row := range rows {
			if existing := byName[row.Name]; existing != nil {
				for _, available := range row.AvailableIn {
					if !containsString(existing.AvailableIn, available) {
						existing.AvailableIn = append(existing.AvailableIn, available)
					}
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

func parseScalewayDediboxOffers(data json.RawMessage, zone string) ([]ServerType, error) {
	var response struct {
		Offers []struct {
			ID               any    `json:"id"`
			Name             string `json:"name"`
			PaymentFrequency string `json:"payment_frequency"`
			Pricing          struct {
				Currency string `json:"currency_code"`
				Units    int64  `json:"units"`
				Nanos    int64  `json:"nanos"`
			} `json:"pricing"`
			ServerInfo *struct {
				Stock string `json:"stock"`
				CPUs  []struct {
					Name        string `json:"name"`
					CoreCount   int    `json:"core_count"`
					ThreadCount int    `json:"thread_count"`
				} `json:"cpus"`
				Memories []struct {
					Capacity int64 `json:"capacity"`
				} `json:"memories"`
				Disks []struct {
					Capacity int64  `json:"capacity"`
					Type     string `json:"type"`
				} `json:"disks"`
			} `json:"server_info"`
		} `json:"offers"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode Scaleway Dedibox offers: %w", err)
	}
	out := make([]ServerType, 0, len(response.Offers))
	for _, offer := range response.Offers {
		id := anyToString(offer.ID)
		if id == "" || offer.ServerInfo == nil || strings.EqualFold(offer.ServerInfo.Stock, "empty") {
			continue
		}
		cores, threads := 0, 0
		cpuNames := make([]string, 0, len(offer.ServerInfo.CPUs))
		for _, cpu := range offer.ServerInfo.CPUs {
			cores += cpu.CoreCount
			threads += cpu.ThreadCount
			if cpu.Name != "" {
				cpuNames = append(cpuNames, cpu.Name)
			}
		}
		var memoryBytes, diskBytes int64
		for _, memory := range offer.ServerInfo.Memories {
			memoryBytes += memory.Capacity
		}
		diskParts := make([]string, 0, len(offer.ServerInfo.Disks))
		for _, disk := range offer.ServerInfo.Disks {
			diskBytes += disk.Capacity
			diskParts = append(diskParts, fmt.Sprintf("%.0f GB %s", bytesToDecimalGB(disk.Capacity), strings.ToUpper(disk.Type)))
		}
		description := firstNonEmpty(offer.Name, "Dedibox "+id)
		details := make([]string, 0, 3)
		if len(cpuNames) > 0 {
			details = append(details, strings.Join(cpuNames, " + "))
		}
		if cores > 0 {
			details = append(details, fmt.Sprintf("%dc/%dt", cores, threads))
		}
		if len(diskParts) > 0 {
			details = append(details, strings.Join(diskParts, " + "))
		}
		if len(details) > 0 {
			description += " — " + strings.Join(details, ", ")
		}
		row := ServerType{
			Name: scalewayDediboxPrefix + id, Description: description,
			Cores: cores, MemoryGB: bytesToGiB(memoryBytes), DiskGB: int(bytesToDecimalGB(diskBytes)),
			CPUType: "dedicated", Architecture: "x86", Platform: "linux", ResourceClass: "bare_metal",
			AvailableIn: []string{zone},
		}
		if strings.EqualFold(offer.Pricing.Currency, "EUR") && strings.EqualFold(offer.PaymentFrequency, "monthly") {
			row.MonthlyPriceEUR = float64(offer.Pricing.Units) + float64(offer.Pricing.Nanos)/1e9
		}
		out = append(out, row)
	}
	return out, nil
}

func scalewayDediboxResources(serverType ServerType) string {
	return marshalJSONString(InstanceResources{
		CPU: &CPUResource{Cores: float64(serverType.Cores)}, MemoryGB: serverType.MemoryGB, DiskGB: serverType.DiskGB,
	}, "{}")
}

func scalewayDediboxProvision(ctx *sdk.AppCtx, in CreateInstanceInput) (*Instance, error) {
	if _, err := scalewayDediboxOfferID(in.Size); err != nil {
		return nil, err
	}
	privKey, pubKey, err := generateSSHKeypair()
	if err != nil {
		return nil, fmt.Errorf("generate ssh keypair: %w", err)
	}
	in.Provider = "scaleway"
	in.Status = "provisioning"
	in.SSHPrivateKey = privKey
	in.SSHPublicKey = pubKey
	in.SSHUser = "apteva"
	in.Platform = "linux"
	in.ResourceClass = "bare_metal"
	if types, catalogErr := scalewayDediboxListServerTypes(ctx); catalogErr == nil {
		for _, serverType := range types {
			if serverType.Name != in.Size {
				continue
			}
			in.ResourcesJSON = scalewayDediboxResources(serverType)
			if in.MonthlyCostCents == 0 {
				in.MonthlyCostCents = int(providerTypePrice(serverType)*100 + 0.5)
			}
			break
		}
	}

	inst, err := dbCreateInstance(ctx.AppDB(), in)
	if err != nil {
		return nil, err
	}
	emitInstanceCreated(ctx, inst)
	emitInstanceStatus(ctx, inst)

	keyMetadata, err := registerScalewaySSHKeyOnConnection(ctx, in.ProviderConnectionID, inst.ID, in.Name, pubKey)
	if err != nil {
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{"status": "error", "error_message": err.Error()})
		return nil, err
	}
	metadata := scalewayDediboxProviderMetadata{
		SSHKeyID: keyMetadata.SSHKeyID, ProjectID: keyMetadata.ProjectID, DesiredImage: in.Image,
	}
	if err := dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"provider_metadata_json": scalewayDediboxMetadataJSON(metadata)}); err != nil {
		_ = deleteScalewaySSHKeyOnConnection(ctx, in.ProviderConnectionID, metadata.SSHKeyID)
		return nil, err
	}

	offerID, _ := scalewayDediboxOfferID(in.Size)
	data, err := executeProviderTool(ctx, "scaleway", "dedibox_server_create", map[string]any{
		"zone": in.Region, "offer_id": offerID, "project_id": metadata.ProjectID, "server_option_ids": []int{},
	})
	if err != nil {
		_ = deleteScalewaySSHKeyOnConnection(ctx, in.ProviderConnectionID, metadata.SSHKeyID)
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{"status": "error", "error_message": err.Error()})
		return nil, err
	}
	serviceID := jsonStringAt(data, "id")
	if serviceID == "" {
		err = errors.New("scaleway.dedibox_server_create response missing service id")
		_ = deleteScalewaySSHKeyOnConnection(ctx, in.ProviderConnectionID, metadata.SSHKeyID)
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{"status": "error", "error_message": err.Error()})
		return nil, err
	}
	metadata.ServiceID = serviceID
	if err := dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{
		"provider_id": "service:" + serviceID, "provider_metadata_json": scalewayDediboxMetadataJSON(metadata),
	}); err != nil {
		_ = scalewayDediboxDeleteService(ctx, in.Region, serviceID)
		_ = deleteScalewaySSHKeyOnConnection(ctx, in.ProviderConnectionID, metadata.SSHKeyID)
		return nil, fmt.Errorf("persist Dedibox service identity: %w; upstream order was cancelled", err)
	}
	kickScalewayDediboxProvisioning(ctx, inst.ID)
	return dbGetInstance(ctx.AppDB(), inst.ID)
}

func kickScalewayDediboxProvisioning(ctx *sdk.AppCtx, instanceID int64) {
	go func() {
		if err := continueScalewayDediboxProvisioning(ctx, instanceID); err != nil {
			fresh, getErr := dbGetInstance(ctx.AppDB(), instanceID)
			if getErr == nil && fresh.Status == "provisioning" {
				_, _ = updateInstanceAndEmit(ctx, instanceID, map[string]any{"status": "error", "error_message": err.Error()})
			}
		}
	}()
}

func continueScalewayDediboxProvisioning(ctx *sdk.AppCtx, instanceID int64) error {
	inst, err := dbGetInstance(ctx.AppDB(), instanceID)
	if err != nil || inst.Status != "provisioning" {
		return err
	}
	metadata := parseScalewayDediboxMetadata(inst.ProviderMetadataJSON)
	if metadata.ServiceID == "" {
		metadata.ServiceID = strings.TrimPrefix(inst.ProviderID, "service:")
	}
	if metadata.ServiceID == "" {
		return errors.New("Dedibox provisioning metadata is missing the service id")
	}

	serverID := inst.ProviderID
	if strings.HasPrefix(serverID, "service:") || serverID == "" {
		serverID, err = waitScalewayDediboxDelivery(ctx, inst.Region, metadata.ServiceID, scalewayDediboxDeliveryTimeout)
		if err != nil {
			return err
		}
		if err := dbUpdateInstance(ctx.AppDB(), instanceID, map[string]any{"provider_id": serverID}); err != nil {
			return err
		}
	}

	serverData, err := executeProviderTool(ctx, "scaleway", "dedibox_server_get", map[string]any{
		"zone": inst.Region, "server_id": numericOrString(serverID),
	})
	if err != nil {
		return err
	}
	_, ipv4, ipv6 := parseProviderResource("scaleway", serverData)
	installedOS := findJSONScalar(serverData, "os_id")
	if installedOS == "" {
		installedOS = nestedDediboxOSID(serverData)
	}
	if installedOS == "" {
		if !metadata.InstallStarted {
			osID, osErr := selectScalewayDediboxOS(ctx, inst.Region, serverID, metadata.ProjectID, metadata.DesiredImage)
			if osErr != nil {
				return osErr
			}
			metadata.InstallStarted = true
			if err := dbUpdateInstance(ctx.AppDB(), instanceID, map[string]any{"provider_metadata_json": scalewayDediboxMetadataJSON(metadata)}); err != nil {
				return err
			}
			_, err = executeProviderTool(ctx, "scaleway", "dedibox_server_install", map[string]any{
				"zone": inst.Region, "server_id": numericOrString(serverID), "os_id": numericOrString(osID),
				"hostname": scalewayDediboxHostname(inst.Name), "user_login": "apteva", "ssh_key_ids": []string{metadata.SSHKeyID},
			})
			if err != nil {
				metadata.InstallStarted = false
				_ = dbUpdateInstance(ctx.AppDB(), instanceID, map[string]any{"provider_metadata_json": scalewayDediboxMetadataJSON(metadata)})
				return err
			}
		}
		if err := waitScalewayDediboxInstall(ctx, inst.Region, serverID, scalewayDediboxInstallTimeout); err != nil {
			return err
		}
	}

	deadline := time.Now().Add(10 * time.Minute)
	for ipv4 == "" && ipv6 == "" {
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for Dedibox public IP")
		}
		time.Sleep(scalewayDediboxPollInterval)
		serverData, err = executeProviderTool(ctx, "scaleway", "dedibox_server_get", map[string]any{
			"zone": inst.Region, "server_id": numericOrString(serverID),
		})
		if err != nil {
			return err
		}
		_, ipv4, ipv6 = parseProviderResource("scaleway", serverData)
	}
	if err := dbUpdateInstance(ctx.AppDB(), instanceID, map[string]any{"public_ipv4": ipv4, "public_ipv6": ipv6}); err != nil {
		return err
	}
	fresh, err := dbGetInstance(ctx.AppDB(), instanceID)
	if err != nil {
		return err
	}
	if err := probeSSHReadyFn(fresh, 15*time.Minute); err != nil {
		return fmt.Errorf("ssh probe: %w", err)
	}
	_, _, err = transitionInstanceAndEmit(ctx, instanceID, []string{"provisioning"}, "ready", map[string]any{"ready_at": nowUTC(), "error_message": ""})
	return err
}

func waitScalewayDediboxDelivery(ctx *sdk.AppCtx, zone, serviceID string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		data, err := executeProviderTool(ctx, "scaleway", "dedibox_service_get", map[string]any{
			"zone": zone, "service_id": numericOrString(serviceID),
		})
		if err != nil {
			return "", err
		}
		status := strings.ToLower(jsonStringAt(data, "provisioning_status"))
		resourceID := jsonStringAt(data, "resource_id")
		if resourceID != "" && status == "ready" {
			return resourceID, nil
		}
		if status == "error" || status == "expired" || status == "expiring" {
			return "", fmt.Errorf("Dedibox service %s entered %s", serviceID, status)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for Dedibox service %s delivery", serviceID)
		}
		time.Sleep(scalewayDediboxPollInterval)
	}
}

func selectScalewayDediboxOS(ctx *sdk.AppCtx, zone, serverID, projectID, desired string) (string, error) {
	data, err := executeProviderTool(ctx, "scaleway", "dedibox_os_list", map[string]any{
		"zone": zone, "server_id": numericOrString(serverID), "project_id": projectID, "type": "server", "page_size": 100,
	})
	if err != nil {
		return "", err
	}
	var response struct {
		OS []struct {
			ID                    any    `json:"id"`
			Name                  string `json:"name"`
			DisplayName           string `json:"display_name"`
			Version               string `json:"version"`
			Arch                  string `json:"arch"`
			Type                  string `json:"type"`
			AllowSSHKeys          bool   `json:"allow_ssh_keys"`
			RequiresLicense       bool   `json:"requires_license"`
			RequiresPanelPassword bool   `json:"requires_panel_password"`
		} `json:"os"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return "", fmt.Errorf("decode Scaleway Dedibox OS catalog: %w", err)
	}
	wanted := strings.ToLower(strings.TrimPrefix(desired, scalewayDediboxPrefix))
	for _, os := range response.OS {
		text := strings.ToLower(strings.Join([]string{os.Name, os.DisplayName, os.Version, os.Arch}, " "))
		if anyToString(os.ID) == "" || !os.AllowSSHKeys || os.RequiresLicense || os.RequiresPanelPassword {
			continue
		}
		if dediboxOSMatches(text, wanted) {
			return anyToString(os.ID), nil
		}
	}
	return "", fmt.Errorf("Scaleway returned no SSH-key-capable Dedibox OS matching %q for server %s", desired, serverID)
}

func dediboxOSMatches(text, wanted string) bool {
	parts := strings.SplitN(wanted, "-", 2)
	if len(parts) == 0 || !strings.Contains(text, parts[0]) {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	version := parts[1]
	return strings.Contains(text, version) || strings.Contains(text, strings.Split(version, ".")[0])
}

func waitScalewayDediboxInstall(ctx *sdk.AppCtx, zone, serverID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		data, err := executeProviderTool(ctx, "scaleway", "dedibox_install_get", map[string]any{
			"zone": zone, "server_id": numericOrString(serverID),
		})
		if err != nil {
			return err
		}
		status := strings.ToLower(jsonStringAt(data, "status"))
		if status == "installed" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for Dedibox server %s OS installation (last status %q)", serverID, status)
		}
		time.Sleep(scalewayDediboxPollInterval)
	}
}

func scalewayDediboxDestroy(ctx *sdk.AppCtx, inst *Instance) error {
	metadata := parseScalewayDediboxMetadata(inst.ProviderMetadataJSON)
	var err error
	if metadata.ServiceID != "" {
		err = scalewayDediboxDeleteService(ctx, inst.Region, metadata.ServiceID)
	} else if inst.ProviderID != "" && !strings.HasPrefix(inst.ProviderID, "service:") {
		err = scalewayDediboxDeleteServer(ctx, inst.Region, inst.ProviderID)
	}
	if err != nil {
		return err
	}
	return deleteScalewaySSHKeyOnConnection(ctx, inst.ProviderConnectionID, metadata.SSHKeyID)
}

func scalewayDediboxDeleteService(ctx *sdk.AppCtx, zone, serviceID string) error {
	return scalewayDediboxDelete(ctx, "dedibox_service_delete", map[string]any{"zone": zone, "service_id": numericOrString(serviceID)})
}

func scalewayDediboxDeleteServer(ctx *sdk.AppCtx, zone, serverID string) error {
	return scalewayDediboxDelete(ctx, "dedibox_server_delete", map[string]any{"zone": zone, "server_id": numericOrString(serverID)})
}

func scalewayDediboxDelete(ctx *sdk.AppCtx, tool string, args map[string]any) error {
	bound, err := apiProviderBound(ctx, "scaleway")
	if err != nil {
		return err
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, args)
	if err != nil {
		return fmt.Errorf("scaleway.%s: %w", tool, err)
	}
	if res == nil || !res.Success {
		if status := upstreamStatus(res); status == 404 || status == 410 {
			return nil
		}
		return fmt.Errorf("scaleway.%s returned: %s", tool, upstreamErrorString(res))
	}
	return nil
}

func scalewayDediboxMetadataJSON(metadata scalewayDediboxProviderMetadata) string {
	return marshalJSONString(metadata, "{}")
}

func parseScalewayDediboxMetadata(raw string) scalewayDediboxProviderMetadata {
	var metadata scalewayDediboxProviderMetadata
	_ = json.Unmarshal([]byte(raw), &metadata)
	return metadata
}

func nestedDediboxOSID(data json.RawMessage) string {
	var response struct {
		OS struct {
			ID any `json:"id"`
		} `json:"os"`
	}
	if json.Unmarshal(data, &response) != nil {
		return ""
	}
	return anyToString(response.OS.ID)
}

func scalewayDediboxHostname(name string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			out.WriteRune(r)
		} else {
			out.WriteByte('-')
		}
	}
	hostname := strings.Trim(out.String(), "-")
	if hostname == "" {
		hostname = "apteva-instance"
	}
	if len(hostname) > 63 {
		hostname = strings.TrimRight(hostname[:63], "-")
	}
	return hostname
}

func bytesToDecimalGB(value int64) float64 {
	return float64(value) / 1_000_000_000
}
