package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// apiProviderSlugs are REST-backed VPS integrations that share the same
// Instances lifecycle but expose different request and response shapes.
var apiProviderSlugs = []string{
	"contabo",
	"vultr",
	"aws-ec2",
	"scaleway",
	"huawei-cloud",
	"linode",
	"ovhcloud",
}

func isAPIProvider(provider string) bool {
	for _, slug := range apiProviderSlugs {
		if provider == slug {
			return true
		}
	}
	return false
}

func apiProviderBound(ctx *sdk.AppCtx, provider string) (*sdk.BoundIntegration, error) {
	bound := ctx.IntegrationFor("provider")
	if bound == nil || bound.ConnectionID == 0 {
		return nil, fmt.Errorf("no VPS provider bound - bind a %s connection on the Instances install", provider)
	}
	if bound.AppSlug != "" && normalizeProvider(bound.AppSlug) != provider {
		return nil, fmt.Errorf("%s adapter requires provider=%s; bound slug is %q", provider, provider, bound.AppSlug)
	}
	return bound, nil
}

func executeProviderTool(ctx *sdk.AppCtx, provider, tool string, args map[string]any) (json.RawMessage, error) {
	bound, err := apiProviderBound(ctx, provider)
	if err != nil {
		return nil, err
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, args)
	if err != nil {
		return nil, fmt.Errorf("%s.%s: %w", provider, tool, err)
	}
	if res == nil || !res.Success {
		return nil, fmt.Errorf("%s.%s returned status=%d: %s", provider, tool, upstreamStatus(res), upstreamErrorString(res))
	}
	return res.Data, nil
}

func apiProviderListServerTypes(ctx *sdk.AppCtx, provider string) ([]ServerType, error) {
	if provider == "contabo" {
		return contaboServerTypes(), nil
	}
	tool, args := "", map[string]any{}
	switch provider {
	case "vultr":
		tool, args = "list_plans", map[string]any{"type": "vc2", "per_page": 500}
	case "aws-ec2":
		tool, args = "list_instance_types", map[string]any{"Action": "DescribeInstanceTypes", "Version": "2016-11-15", "MaxResults": 100}
	case "scaleway":
		tool, args = "server_types_list", map[string]any{"zone": "fr-par-1", "per_page": 100}
	case "huawei-cloud":
		tool, args = "list_flavors", map[string]any{}
	case "linode":
		tool, args = "list_types", map[string]any{"page_size": 500}
	case "ovhcloud":
		tool, args = "list_flavors", map[string]any{}
	default:
		return nil, providerAdapterUnavailable(provider, "server type catalog")
	}
	data, err := executeProviderTool(ctx, provider, tool, args)
	if err != nil {
		return nil, err
	}
	types, err := parseProviderServerTypes(provider, data)
	if err != nil {
		return nil, err
	}
	for i := range types {
		if types[i].Platform == "" {
			types[i].Platform = "linux"
		}
		if types[i].ResourceClass == "" {
			types[i].ResourceClass = "virtual"
		}
	}
	if provider == "scaleway" {
		appleData, appleErr := executeProviderTool(ctx, provider, "apple_products_list", map[string]any{
			"product_types": []string{"apple_silicon"}, "page_size": 100,
		})
		if appleErr == nil {
			appleTypes, parseErr := parseScalewayAppleProducts(appleData)
			if parseErr != nil {
				return nil, parseErr
			}
			types = append(types, appleTypes...)
		} else {
			ctx.Logger().Warn("instances: Scaleway Apple silicon catalog unavailable", "err", appleErr)
		}
	}
	sort.SliceStable(types, func(i, j int) bool {
		pi, pj := providerTypePrice(types[i]), providerTypePrice(types[j])
		if pi > 0 && pj > 0 && pi != pj {
			return pi < pj
		}
		if types[i].MemoryGB != types[j].MemoryGB {
			return types[i].MemoryGB < types[j].MemoryGB
		}
		return types[i].Name < types[j].Name
	})
	return activeServerTypes(types), nil
}

func apiProviderListLocations(ctx *sdk.AppCtx, provider string) ([]Location, error) {
	if provider == "contabo" {
		return contaboLocations(), nil
	}
	if provider == "scaleway" {
		return scalewayLocations(), nil
	}
	tool, args := "", map[string]any{}
	switch provider {
	case "vultr":
		tool, args = "list_regions", map[string]any{"per_page": 500}
	case "aws-ec2":
		tool, args = "list_availability_zones", map[string]any{"Action": "DescribeAvailabilityZones", "Version": "2016-11-15", "AllAvailabilityZones": false}
	case "huawei-cloud":
		tool = "list_availability_zones"
	case "linode":
		tool, args = "list_regions", map[string]any{"page_size": 500}
	case "ovhcloud":
		tool = "list_regions"
	default:
		return nil, providerAdapterUnavailable(provider, "location catalog")
	}
	data, err := executeProviderTool(ctx, provider, tool, args)
	if err != nil {
		return nil, err
	}
	return parseProviderLocations(provider, data)
}

func apiProviderListImages(ctx *sdk.AppCtx, provider string) ([]Image, error) {
	tool, args := "", map[string]any{}
	switch provider {
	case "contabo":
		tool, args = "image_list", map[string]any{"request_id": newRequestID(), "page": 1, "size": 100, "standardImage": true}
	case "vultr":
		tool, args = "list_os", map[string]any{"per_page": 500}
	case "aws-ec2":
		tool, args = "list_images", map[string]any{
			"Action": "DescribeImages", "Version": "2016-11-15", "Owner.1": "amazon",
			"Filter.1.Name": "name", "Filter.1.Value.1": "ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*",
			"Filter.2.Name": "state", "Filter.2.Value.1": "available",
			"Filter.3.Name": "architecture", "Filter.3.Value.1": "x86_64",
		}
	case "scaleway":
		tool, args = "image_list", map[string]any{"zone": "fr-par-1", "per_page": 100, "public": true}
	case "huawei-cloud":
		tool, args = "list_images", map[string]any{"imagetype": "gold", "__os_type": "Linux", "limit": 100}
	case "linode":
		tool, args = "list_images", map[string]any{"page_size": 500}
	case "ovhcloud":
		tool, args = "list_images", map[string]any{"osType": "linux"}
	default:
		return nil, providerAdapterUnavailable(provider, "image catalog")
	}
	data, err := executeProviderTool(ctx, provider, tool, args)
	if err != nil {
		return nil, err
	}
	images, err := parseProviderImages(provider, data)
	if err != nil {
		return nil, err
	}
	for i := range images {
		if images[i].Platform == "" {
			images[i].Platform = "linux"
		}
		if images[i].ResourceClass == "" {
			images[i].ResourceClass = "virtual"
		}
	}
	if provider == "scaleway" {
		for _, zone := range scalewayAppleZones {
			appleData, appleErr := executeProviderTool(ctx, provider, "apple_os_list", map[string]any{"zone": zone, "page_size": 100})
			if appleErr != nil {
				ctx.Logger().Warn("instances: Scaleway Apple silicon OS catalog unavailable", "zone", zone, "err", appleErr)
				continue
			}
			appleImages, parseErr := parseScalewayAppleImages(appleData, zone)
			if parseErr != nil {
				return nil, parseErr
			}
			images = append(images, appleImages...)
		}
		images = mergeCatalogImages(images)
	}
	sort.SliceStable(images, func(i, j int) bool { return images[i].Description < images[j].Description })
	return images, nil
}

func apiProviderProvision(ctx *sdk.AppCtx, in CreateInstanceInput) (*Instance, error) {
	provider := normalizeProvider(in.Provider)
	if !isAPIProvider(provider) {
		return nil, providerAdapterUnavailable(provider, "provisioning")
	}
	if _, err := apiProviderBound(ctx, provider); err != nil {
		return nil, err
	}

	if err := applyAPIProviderDefaults(ctx, provider, &in); err != nil {
		return nil, err
	}
	privKey, pubKey, err := generateSSHKeypair()
	if err != nil {
		return nil, fmt.Errorf("generate ssh keypair: %w", err)
	}
	in.Provider = provider
	in.Status = "provisioning"
	in.SSHPrivateKey = privKey
	in.SSHPublicKey = pubKey
	in.SSHUser = "root"
	in.Platform = "linux"
	in.ResourceClass = "virtual"
	appleMac := provider == "scaleway" && isScalewayAppleSize(in.Size)
	if appleMac {
		in.Platform = "macos"
		in.ResourceClass = "bare_metal"
		if types, catalogErr := apiProviderListServerTypes(ctx, provider); catalogErr == nil {
			for _, serverType := range types {
				if serverType.Name != in.Size {
					continue
				}
				in.Platform = serverType.Platform
				in.ResourceClass = serverType.ResourceClass
				in.ResourcesJSON = scalewayAppleResources(serverType)
				if in.MonthlyCostCents == 0 {
					in.MonthlyCostCents = int(providerTypePrice(serverType)*100 + 0.5)
				}
				break
			}
		}
	}
	if in.MonthlyCostCents == 0 {
		in.MonthlyCostCents = apiProviderMonthlyCostCents(ctx, provider, in.Size)
	}

	inst, err := dbCreateInstance(ctx.AppDB(), in)
	if err != nil {
		return nil, err
	}
	emitInstanceCreated(ctx, inst)
	emitInstanceStatus(ctx, inst)

	tool, args := "", map[string]any(nil)
	if appleMac {
		metadata, accessErr := registerScalewayAppleSSHKey(ctx, inst.ID, in.Name, pubKey)
		if accessErr != nil {
			_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{"status": "error", "error_message": accessErr.Error()})
			return nil, accessErr
		}
		inst.ProviderMetadataJSON = scalewayAppleMetadataJSON(metadata)
		if err := dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"provider_metadata_json": inst.ProviderMetadataJSON}); err != nil {
			cleanupErr := deleteScalewayAppleSSHKey(ctx, metadata.SSHKeyID)
			return nil, fmt.Errorf("persist Scaleway Mac SSH key: %w (cleanup: %v)", err, cleanupErr)
		}
		tool, args = "apple_server_create", map[string]any{
			"zone": in.Region, "project_id": metadata.ProjectID, "name": in.Name,
			"type": scalewayAppleID(in.Size), "os_id": scalewayAppleID(in.Image),
			"commitment_type": "duration_24h", "enable_vpc": false, "enable_kext": false,
		}
	} else {
		tool, args, err = apiProviderCreateRequest(ctx, provider, in, pubKey)
	}
	if err != nil {
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{"status": "error", "error_message": err.Error()})
		return nil, err
	}
	data, err := executeProviderTool(ctx, provider, tool, args)
	if err != nil {
		if appleMac {
			if cleanupErr := deleteScalewayAppleSSHKey(ctx, parseScalewayAppleMetadata(inst.ProviderMetadataJSON).SSHKeyID); cleanupErr != nil {
				ctx.Logger().Error("instances: failed to clean up Scaleway Mac SSH key after create failure", "id", inst.ID, "err", cleanupErr)
			}
		}
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{"status": "error", "error_message": err.Error()})
		return nil, err
	}
	provID, ipv4, ipv6, err := resolveProviderCreateResponse(ctx, provider, in, data)
	if err != nil || provID == "" {
		if err == nil {
			err = fmt.Errorf("%s.%s response missing provider id", provider, tool)
		}
		if appleMac {
			if cleanupErr := deleteScalewayAppleSSHKey(ctx, parseScalewayAppleMetadata(inst.ProviderMetadataJSON).SSHKeyID); cleanupErr != nil {
				ctx.Logger().Error("instances: failed to clean up Scaleway Mac SSH key after invalid create response", "id", inst.ID, "err", cleanupErr)
			}
		}
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{"status": "error", "error_message": err.Error()})
		return nil, err
	}
	persistFields := map[string]any{"provider_id": provID, "public_ipv4": ipv4, "public_ipv6": ipv6}
	if appleMac {
		for key, value := range scalewayAppleResponseFields(data) {
			persistFields[key] = value
		}
	}
	if err := dbUpdateInstance(ctx.AppDB(), inst.ID, persistFields); err != nil {
		orphan := *inst
		orphan.ProviderID, orphan.PublicIPv4, orphan.PublicIPv6 = provID, ipv4, ipv6
		cleanupErr := apiProviderDestroy(ctx, &orphan)
		ctx.Logger().Error("instances: failed to persist provider identity", "provider", provider, "id", inst.ID, "provider_id", provID, "err", err, "cleanup_err", cleanupErr)
		if cleanupErr != nil {
			return nil, fmt.Errorf("persist created %s instance %s: %w; automatic cleanup also failed: %v", provider, provID, err, cleanupErr)
		}
		return nil, fmt.Errorf("persist created %s instance %s: %w; upstream instance was cleaned up", provider, provID, err)
	}

	if provider == "scaleway" && !appleMac {
		_, err = executeProviderTool(ctx, provider, "server_set_cloud_init", map[string]any{
			"zone": in.Region, "server_id": provID, "content": buildCloudInit(pubKey),
		})
		if err != nil {
			_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{"status": "error", "error_message": err.Error()})
			return nil, err
		}
		_, err = executeProviderTool(ctx, provider, "server_action", map[string]any{"zone": in.Region, "server_id": provID, "action": "poweron"})
		if err != nil {
			_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{"status": "error", "error_message": err.Error()})
			return nil, err
		}
	}
	kickAPIProviderReadinessProbe(ctx, inst.ID)
	return dbGetInstance(ctx.AppDB(), inst.ID)
}

func applyAPIProviderDefaults(ctx *sdk.AppCtx, provider string, in *CreateInstanceInput) error {
	if in.Size == "" {
		types, err := apiProviderListServerTypes(ctx, provider)
		if err != nil {
			return fmt.Errorf("load %s server types for default: %w", provider, err)
		}
		if len(types) == 0 {
			return fmt.Errorf("%s returned no provisionable server types", provider)
		}
		in.Size = types[0].Name
	}
	if in.Region == "" {
		locations, err := apiProviderListLocations(ctx, provider)
		if err != nil {
			return fmt.Errorf("load %s locations for default: %w", provider, err)
		}
		if len(locations) == 0 {
			return fmt.Errorf("%s returned no provisionable locations", provider)
		}
		in.Region = locations[0].Name
	}
	if in.Image == "" {
		images, err := apiProviderListImages(ctx, provider)
		if err != nil {
			return fmt.Errorf("load %s images for default: %w", provider, err)
		}
		if len(images) == 0 {
			return fmt.Errorf("%s returned no provisionable images", provider)
		}
		if provider == "scaleway" && isScalewayAppleSize(in.Size) {
			for _, image := range images {
				if image.ResourceClass == "bare_metal" &&
					(len(image.AvailableIn) == 0 || containsString(image.AvailableIn, in.Region)) &&
					(len(image.CompatibleTypes) == 0 || containsString(image.CompatibleTypes, in.Size)) {
					in.Image = image.Name
					break
				}
			}
			if in.Image == "" {
				return fmt.Errorf("Scaleway returned no Apple silicon OS compatible with %s in %s", in.Size, in.Region)
			}
		} else {
			in.Image = preferredLinuxImage(images)
		}
	}
	if provider == "scaleway" && isScalewayAppleSize(in.Size) {
		if !containsString(scalewayAppleZones, in.Region) {
			return fmt.Errorf("Scaleway Apple silicon is not available in %s; choose one of %s", in.Region, strings.Join(scalewayAppleZones, ", "))
		}
		if !strings.HasPrefix(in.Image, scalewayApplePrefix) {
			return fmt.Errorf("Scaleway Apple silicon size %s requires a compatible Apple silicon OS image", in.Size)
		}
	}
	return nil
}

func preferredLinuxImage(images []Image) string {
	for _, image := range images {
		text := strings.ToLower(image.Name + " " + image.Description + " " + image.OSFlavor + " " + image.OSVersion)
		if strings.Contains(text, "ubuntu") && strings.Contains(text, "24.04") && (image.Architecture == "" || strings.Contains(strings.ToLower(image.Architecture), "x86")) {
			return image.Name
		}
	}
	return images[0].Name
}

func apiProviderCreateRequest(ctx *sdk.AppCtx, provider string, in CreateInstanceInput, pubKey string) (string, map[string]any, error) {
	cloudInit := buildCloudInit(pubKey)
	switch provider {
	case "contabo":
		return "instance_create", map[string]any{
			"request_id": newRequestID(), "imageId": in.Image, "productId": in.Size, "region": in.Region,
			"userData": cloudInit, "displayName": in.Name, "period": 1, "defaultUser": "root",
		}, nil
	case "vultr":
		osID, err := strconv.Atoi(in.Image)
		if err != nil {
			return "", nil, fmt.Errorf("vultr image must be a numeric OS id, got %q", in.Image)
		}
		return "create_instance", map[string]any{"region": in.Region, "plan": in.Size, "os_id": osID, "label": in.Name, "hostname": in.Name, "user_data": base64.StdEncoding.EncodeToString([]byte(cloudInit)), "enable_ipv6": true}, nil
	case "aws-ec2":
		return "create_instance", map[string]any{
			"Action": "RunInstances", "Version": "2016-11-15", "ImageId": in.Image, "InstanceType": in.Size,
			"MinCount": 1, "MaxCount": 1, "Placement.AvailabilityZone": in.Region,
			"UserData":                        base64.StdEncoding.EncodeToString([]byte(cloudInit)),
			"TagSpecification.1.ResourceType": "instance", "TagSpecification.1.Tag.1.Key": "Name", "TagSpecification.1.Tag.1.Value": in.Name,
		}, nil
	case "scaleway":
		project, err := scalewayDefaultProject(ctx)
		if err != nil {
			return "", nil, err
		}
		return "server_create", map[string]any{
			"zone": in.Region, "project": project, "name": in.Name, "commercial_type": in.Size, "image": in.Image,
			"dynamic_ip_required": true, "routed_ip_enabled": true, "enable_ipv6": true,
		}, nil
	case "huawei-cloud":
		vpcID, subnetID, err := huaweiDefaultNetwork(ctx)
		if err != nil {
			return "", nil, err
		}
		server := map[string]any{
			"name": in.Name, "imageRef": in.Image, "flavorRef": in.Size, "availability_zone": in.Region,
			"vpcid": vpcID, "nics": []map[string]any{{"subnet_id": subnetID}}, "root_volume": map[string]any{"volumetype": "SAS"},
			"count": 1, "user_data": base64.StdEncoding.EncodeToString([]byte(cloudInit)),
			"publicip": map[string]any{"eip": map[string]any{"iptype": "5_bgp", "bandwidth": map[string]any{"size": 1, "sharetype": "PER", "chargemode": "traffic", "name": in.Name + "-bandwidth"}}, "delete_on_termination": true},
		}
		return "create_server", map[string]any{"server": server}, nil
	case "linode":
		password, err := randomPassword()
		if err != nil {
			return "", nil, err
		}
		return "create_instance", map[string]any{"type": in.Size, "region": in.Region, "image": in.Image, "label": in.Name, "root_pass": password, "authorized_keys": []string{pubKey}, "backups_enabled": false}, nil
	case "ovhcloud":
		return "create_instance", map[string]any{"name": in.Name, "region": in.Region, "flavorId": in.Size, "imageId": in.Image, "userData": cloudInit, "monthlyBilling": false}, nil
	default:
		return "", nil, providerAdapterUnavailable(provider, "provisioning")
	}
}

func resolveProviderCreateResponse(ctx *sdk.AppCtx, provider string, in CreateInstanceInput, data json.RawMessage) (string, string, string, error) {
	if provider != "huawei-cloud" {
		id, ipv4, ipv6 := parseProviderResource(provider, data)
		return id, ipv4, ipv6, nil
	}
	jobID := jsonStringAt(data, "job_id")
	if jobID == "" {
		id, ipv4, ipv6 := parseProviderResource(provider, data)
		return id, ipv4, ipv6, nil
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		jobData, err := executeProviderTool(ctx, provider, "get_job", map[string]any{"job_id": jobID})
		if err != nil {
			return "", "", "", err
		}
		status := strings.ToUpper(jsonStringAt(jobData, "status"))
		if status == "SUCCESS" {
			id := findJSONScalar(jobData, "server_id")
			if id == "" {
				return "", "", "", errors.New("huawei-cloud create job succeeded without a server_id")
			}
			return id, "", "", nil
		}
		if status == "FAIL" || status == "FAILED" {
			return "", "", "", fmt.Errorf("huawei-cloud create job failed: %s", findJSONScalar(jobData, "fail_reason"))
		}
		if time.Now().After(deadline) {
			return "", "", "", errors.New("timed out waiting for Huawei Cloud create job")
		}
		time.Sleep(5 * time.Second)
	}
}

func apiProviderDestroy(ctx *sdk.AppCtx, inst *Instance) error {
	provider := normalizeProvider(inst.Provider)
	if isScalewayAppleInstance(inst) {
		if inst.ProviderID == "" {
			return deleteScalewayAppleSSHKey(ctx, parseScalewayAppleMetadata(inst.ProviderMetadataJSON).SSHKeyID)
		}
		return scalewayAppleDestroy(ctx, inst)
	}
	if inst.ProviderID == "" {
		return nil
	}
	tool, args := "", map[string]any{}
	switch provider {
	case "contabo":
		return errors.New("contabo does not support immediate instance deletion; cancel the service from Contabo and remove it from Instances only after it has terminated")
	case "vultr":
		tool, args = "delete_instance", map[string]any{"instance_id": inst.ProviderID}
	case "aws-ec2":
		tool, args = "terminate_instance", map[string]any{"Action": "TerminateInstances", "Version": "2016-11-15", "InstanceId.1": inst.ProviderID}
	case "scaleway":
		tool, args = "server_delete", map[string]any{"zone": inst.Region, "server_id": inst.ProviderID}
	case "huawei-cloud":
		tool, args = "delete_servers", map[string]any{"servers": []map[string]any{{"id": inst.ProviderID}}, "delete_publicip": true, "delete_volume": true}
	case "linode":
		tool, args = "delete_instance", map[string]any{"linodeId": inst.ProviderID}
	case "ovhcloud":
		tool, args = "delete_instance", map[string]any{"instanceId": inst.ProviderID}
	default:
		return providerAdapterUnavailable(provider, "destroy")
	}
	bound, err := apiProviderBound(ctx, provider)
	if err != nil {
		return err
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, args)
	if err != nil {
		return fmt.Errorf("%s.%s: %w", provider, tool, err)
	}
	if res == nil || !res.Success {
		if status := upstreamStatus(res); status == 404 || status == 410 {
			return nil
		}
		return fmt.Errorf("%s.%s returned: %s", provider, tool, upstreamErrorString(res))
	}
	return nil
}

func kickAPIProviderReadinessProbe(ctx *sdk.AppCtx, id int64) {
	go func() {
		fresh, err := dbGetInstance(ctx.AppDB(), id)
		if err != nil || fresh.Status != "provisioning" {
			return
		}
		if fresh.PublicIPv4 == "" && fresh.PublicIPv6 == "" {
			fresh, err = waitAPIProviderNetwork(ctx, fresh, 5*time.Minute)
			if err != nil {
				_, _ = updateInstanceAndEmit(ctx, id, map[string]any{"status": "error", "error_message": fmt.Sprintf("%s network: %v", fresh.Provider, err)})
				return
			}
		}
		if err := probeSSHReadyFn(fresh, 5*time.Minute); err != nil {
			_, _ = updateInstanceAndEmit(ctx, id, map[string]any{"status": "error", "error_message": fmt.Sprintf("ssh probe: %v", err)})
			return
		}
		_, _, _ = transitionInstanceAndEmit(ctx, id, []string{"provisioning"}, "ready", map[string]any{"ready_at": nowUTC(), "error_message": ""})
	}()
}

func waitAPIProviderNetwork(ctx *sdk.AppCtx, inst *Instance, timeout time.Duration) (*Instance, error) {
	deadline := time.Now().Add(timeout)
	for {
		tool, args, err := apiProviderGetRequest(inst)
		if err != nil {
			return nil, err
		}
		data, err := executeProviderTool(ctx, inst.Provider, tool, args)
		if err != nil {
			return nil, err
		}
		_, ipv4, ipv6 := parseProviderResource(inst.Provider, data)
		if ipv4 != "" || ipv6 != "" {
			fields := map[string]any{"public_ipv4": ipv4, "public_ipv6": ipv6}
			if isScalewayAppleInstance(inst) {
				for key, value := range scalewayAppleResponseFields(data) {
					fields[key] = value
				}
			}
			if err := dbUpdateInstance(ctx.AppDB(), inst.ID, fields); err != nil {
				return nil, err
			}
			return dbGetInstance(ctx.AppDB(), inst.ID)
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out waiting for public IP")
		}
		time.Sleep(5 * time.Second)
	}
}

func apiProviderGetRequest(inst *Instance) (string, map[string]any, error) {
	switch inst.Provider {
	case "contabo":
		return "instance_get", map[string]any{"request_id": newRequestID(), "instanceId": numericOrString(inst.ProviderID)}, nil
	case "vultr":
		return "get_instance", map[string]any{"instance_id": inst.ProviderID}, nil
	case "aws-ec2":
		return "list_instances", map[string]any{"Action": "DescribeInstances", "Version": "2016-11-15", "InstanceId.1": inst.ProviderID}, nil
	case "scaleway":
		if isScalewayAppleInstance(inst) {
			return "apple_server_get", map[string]any{"zone": inst.Region, "server_id": inst.ProviderID}, nil
		}
		return "server_get", map[string]any{"zone": inst.Region, "server_id": inst.ProviderID}, nil
	case "huawei-cloud":
		return "get_server", map[string]any{"server_id": inst.ProviderID}, nil
	case "linode":
		return "get_instance", map[string]any{"linodeId": inst.ProviderID}, nil
	case "ovhcloud":
		return "get_instance", map[string]any{"instanceId": inst.ProviderID}, nil
	default:
		return "", nil, providerAdapterUnavailable(inst.Provider, "instance lookup")
	}
}

func reconcileAPIProviderProvisioning(ctx *sdk.AppCtx) {
	for _, provider := range apiProviderSlugs {
		rows, err := dbListInstances(ctx.AppDB(), provider, "provisioning")
		if err != nil {
			ctx.Logger().Warn("instances: reconcile provider list failed", "provider", provider, "err", err)
			continue
		}
		for _, inst := range rows {
			if inst.ProviderID != "" {
				kickAPIProviderReadinessProbe(ctx, inst.ID)
				continue
			}
			_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{
				"status":        "error",
				"error_message": "provisioning interrupted before the provider id was recorded - Instances will not infer a resource by name; check the " + provider + " dashboard for an orphan named " + inst.Name,
			})
		}
	}
}

func apiProviderMonthlyCostCents(ctx *sdk.AppCtx, provider, size string) int {
	types, err := apiProviderListServerTypes(ctx, provider)
	if err != nil {
		return 0
	}
	for _, t := range types {
		if t.Name == size {
			return int(providerTypePrice(t)*100 + 0.5)
		}
	}
	return 0
}

func providerTypePrice(t ServerType) float64 {
	if t.MonthlyPriceEUR > 0 {
		return t.MonthlyPriceEUR
	}
	return t.MonthlyPriceUSD
}

func scalewayDefaultProject(ctx *sdk.AppCtx) (string, error) {
	data, err := executeProviderTool(ctx, "scaleway", "project_list", map[string]any{"page_size": 100})
	if err != nil {
		return "", err
	}
	id := firstJSONScalar(data, "projects", "id")
	if id == "" {
		return "", errors.New("scaleway account has no project available for provisioning")
	}
	return id, nil
}

func huaweiDefaultNetwork(ctx *sdk.AppCtx) (string, string, error) {
	vpcData, err := executeProviderTool(ctx, "huawei-cloud", "list_vpcs", map[string]any{"limit": 100})
	if err != nil {
		return "", "", err
	}
	vpcID := firstJSONScalar(vpcData, "vpcs", "id")
	if vpcID == "" {
		return "", "", errors.New("huawei-cloud account has no VPC; create one before provisioning an instance")
	}
	subnetData, err := executeProviderTool(ctx, "huawei-cloud", "list_subnets", map[string]any{"vpc_id": vpcID, "limit": 100})
	if err != nil {
		return "", "", err
	}
	subnetID := firstJSONScalar(subnetData, "subnets", "id")
	if subnetID == "" {
		return "", "", fmt.Errorf("huawei-cloud VPC %s has no subnet", vpcID)
	}
	return vpcID, subnetID, nil
}

func randomPassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate provider root password: %w", err)
	}
	return "Aa1!" + base64.RawURLEncoding.EncodeToString(b), nil
}

func newRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%08x-0000-4000-8000-%012x", time.Now().UnixNano(), time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func numericOrString(value string) any {
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		return n
	}
	return value
}
