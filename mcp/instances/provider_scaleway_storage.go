package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type scalewayBootVolume struct {
	ID           string
	ProviderType string
	StorageClass string
	SizeGB       int
}

func scalewayBootProviderType(boot *BootStorageRequest) string {
	if boot == nil {
		return ""
	}
	if boot.StorageClass == "local" || boot.ProviderType == "l_ssd" || boot.Tier == "local" {
		return "l_ssd"
	}
	return "sbs_volume"
}

func validateScalewayBootStorage(ctx *sdk.AppCtx, in *CreateInstanceInput) error {
	if in == nil || in.Storage.Boot == nil {
		return nil
	}
	boot := in.Storage.Boot
	expectedType := scalewayBootProviderType(boot)
	if boot.ProviderType != "" {
		switch boot.ProviderType {
		case "l_ssd":
			if boot.StorageClass != "" && boot.StorageClass != "local" {
				return errors.New("Scaleway provider_type l_ssd requires storage_class local")
			}
		case "sbs_volume", "sbs_5k", "sbs_15k":
			if boot.StorageClass == "local" {
				return fmt.Errorf("Scaleway provider_type %s requires storage_class block", boot.ProviderType)
			}
		default:
			return fmt.Errorf("unsupported Scaleway boot provider_type %q; use l_ssd or sbs_volume", boot.ProviderType)
		}
	}
	if expectedType == "l_ssd" {
		boot.StorageClass, boot.ProviderType, boot.Tier = "local", "l_ssd", "local"
	} else {
		boot.StorageClass, boot.ProviderType = "block", "sbs_volume"
		if boot.Tier == "" || boot.Tier == "local" {
			boot.Tier = "balanced"
		}
	}

	types, err := apiProviderListServerTypes(ctx, "scaleway")
	if err != nil {
		return fmt.Errorf("load Scaleway server storage constraints: %w", err)
	}
	var selected *ServerType
	for i := range types {
		if types[i].Name == in.Size {
			selected = &types[i]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("Scaleway server type %q is not present in the live catalog", in.Size)
	}
	var constraint *StorageConstraint
	for i := range selected.BootStorage {
		if selected.BootStorage[i].StorageClass == boot.StorageClass {
			constraint = &selected.BootStorage[i]
			break
		}
	}
	if constraint == nil {
		return fmt.Errorf("Scaleway server type %s does not support %s boot storage", in.Size, boot.StorageClass)
	}
	if constraint.MinSizeGB > 0 && boot.SizeGB < constraint.MinSizeGB {
		return fmt.Errorf("Scaleway %s %s boot storage requires at least %d GB", in.Size, boot.StorageClass, constraint.MinSizeGB)
	}
	if constraint.MaxSizeGB > 0 && boot.SizeGB > constraint.MaxSizeGB {
		return fmt.Errorf("Scaleway %s %s boot storage supports at most %d GB", in.Size, boot.StorageClass, constraint.MaxSizeGB)
	}

	images, err := apiProviderListImages(ctx, "scaleway")
	if err != nil {
		return fmt.Errorf("load Scaleway image storage compatibility: %w", err)
	}
	for _, image := range images {
		if image.Name != in.Image {
			continue
		}
		if image.ProviderType != "" && image.ProviderType != expectedType {
			return fmt.Errorf("Scaleway image %s is backed by %s and cannot create a %s boot volume; select the %s-compatible image variant", image.Description, image.ProviderType, expectedType, expectedType)
		}
		return nil
	}
	return fmt.Errorf("Scaleway image %q is not present in the live catalog", in.Image)
}

func verifyAndPersistScalewayBootStorage(ctx *sdk.AppCtx, inst *Instance) (*scalewayBootVolume, error) {
	serverData, err := executeProviderToolOnConnection(ctx, inst.ProviderConnectionID, "scaleway", "server_get", map[string]any{
		"zone": inst.Region, "server_id": inst.ProviderID,
	})
	if err != nil {
		return nil, fmt.Errorf("verify Scaleway boot storage: %w", err)
	}
	actual, err := parseScalewayBootVolume(serverData)
	if err != nil {
		return nil, err
	}
	tool := "volume_get"
	if actual.ProviderType == "l_ssd" {
		tool = "instance_volume_get"
	}
	volumeData, err := executeProviderToolOnConnection(ctx, inst.ProviderConnectionID, "scaleway", tool, map[string]any{
		"zone": inst.Region, "volume_id": actual.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("verify Scaleway boot volume %s: %w", actual.ID, err)
	}
	sizeBytes, err := strconv.ParseInt(findJSONScalar(volumeData, "size"), 10, 64)
	if err != nil || sizeBytes <= 0 {
		return nil, fmt.Errorf("verify Scaleway boot volume %s: provider response missing a positive size", actual.ID)
	}
	actual.SizeGB = decimalGBInt(sizeBytes)
	requested, err := bootStorageRequestFromInstance(inst)
	if err != nil {
		return nil, err
	}
	if requested != nil {
		expectedType := scalewayBootProviderType(requested)
		if actual.ProviderType != expectedType {
			return nil, fmt.Errorf("Scaleway boot storage mismatch: requested %s, provider created %s", expectedType, actual.ProviderType)
		}
		if actual.SizeGB != requested.SizeGB {
			return nil, fmt.Errorf("Scaleway boot storage mismatch: requested %d GB, provider created %d GB", requested.SizeGB, actual.SizeGB)
		}
	}
	if err := persistScalewayBootVolume(ctx.AppDB(), inst, actual, requested); err != nil {
		return nil, fmt.Errorf("persist verified Scaleway boot storage: %w", err)
	}
	return actual, nil
}

func parseScalewayBootVolume(data json.RawMessage) (*scalewayBootVolume, error) {
	var payload struct {
		Server struct {
			Volumes map[string]struct {
				ID         string `json:"id"`
				VolumeType string `json:"volume_type"`
				Boot       bool   `json:"boot"`
			} `json:"volumes"`
		} `json:"server"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode Scaleway server volumes: %w", err)
	}
	keys := make([]string, 0, len(payload.Server.Volumes))
	for key := range payload.Server.Volumes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	selectedKey := ""
	for _, key := range keys {
		if payload.Server.Volumes[key].Boot {
			selectedKey = key
			break
		}
	}
	if selectedKey == "" {
		if _, ok := payload.Server.Volumes["0"]; ok {
			selectedKey = "0"
		} else if len(keys) > 0 {
			selectedKey = keys[0]
		}
	}
	if selectedKey == "" {
		return nil, errors.New("verify Scaleway boot storage: server has no attached volumes")
	}
	volume := payload.Server.Volumes[selectedKey]
	providerType := scalewayImageProviderType(volume.VolumeType)
	if providerType == "" {
		providerType = strings.ToLower(strings.TrimSpace(volume.VolumeType))
	}
	if volume.ID == "" || (providerType != "l_ssd" && providerType != "sbs_volume") {
		return nil, fmt.Errorf("verify Scaleway boot storage: invalid volume id/type at slot %s", selectedKey)
	}
	storageClass := "block"
	if providerType == "l_ssd" {
		storageClass = "local"
	}
	return &scalewayBootVolume{ID: volume.ID, ProviderType: providerType, StorageClass: storageClass}, nil
}

func bootStorageRequestFromInstance(inst *Instance) (*BootStorageRequest, error) {
	if inst == nil || strings.TrimSpace(inst.StorageJSON) == "" || strings.TrimSpace(inst.StorageJSON) == "{}" {
		return nil, nil
	}
	var storage InstanceStorageRequest
	if err := json.Unmarshal([]byte(inst.StorageJSON), &storage); err != nil {
		return nil, fmt.Errorf("decode requested boot storage: %w", err)
	}
	return storage.Boot, nil
}

func persistScalewayBootVolume(db *sql.DB, inst *Instance, actual *scalewayBootVolume, requested *BootStorageRequest) error {
	if db == nil || inst == nil || actual == nil {
		return errors.New("boot-volume persistence requires database, instance, and actual volume")
	}
	tier, deletePolicy := "provider-default", "with_instance"
	if requested != nil {
		tier = requested.Tier
		deletePolicy = requested.DeletePolicy
	}
	if tier == "" {
		tier = "provider-default"
	}
	if deletePolicy == "" {
		deletePolicy = "with_instance"
	}
	storage := InstanceStorageRequest{Boot: &BootStorageRequest{
		SizeGB: actual.SizeGB, StorageClass: actual.StorageClass, Tier: tier,
		ProviderType: actual.ProviderType, DeletePolicy: deletePolicy,
	}}
	storageJSON, _ := json.Marshal(storage)
	metadataJSON, _ := json.Marshal(map[string]any{"verified_at": nowUTC()})
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE instances SET storage_json=? WHERE id=?`, string(storageJSON), inst.ID); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO instance_volumes
		(instance_id,provider,provider_connection_id,provider_volume_id,name,role,storage_class,tier,provider_type,size_gb,region,status,managed,delete_policy,provider_metadata_json,created_at,updated_at)
		VALUES (?,?,?,?,?,'boot',?,?,?,?,?,'attached',1,?,?,?,?)
		ON CONFLICT(provider_connection_id,provider,provider_volume_id) DO UPDATE SET
		instance_id=excluded.instance_id,name=excluded.name,role='boot',storage_class=excluded.storage_class,
		tier=excluded.tier,provider_type=excluded.provider_type,size_gb=excluded.size_gb,region=excluded.region,
		status='attached',managed=1,delete_policy=excluded.delete_policy,provider_metadata_json=excluded.provider_metadata_json,updated_at=excluded.updated_at`,
		inst.ID, inst.Provider, inst.ProviderConnectionID, actual.ID, inst.Name+"-boot",
		actual.StorageClass, tier, actual.ProviderType, actual.SizeGB, inst.Region,
		deletePolicy, string(metadataJSON), nowUTC(), nowUTC())
	if err != nil {
		return err
	}
	return tx.Commit()
}

var runScalewayRootStorageCommand = runSSH

func verifyScalewayRootFilesystem(inst *Instance, requestedSizeGB int) error {
	if requestedSizeGB <= 0 {
		return nil
	}
	output, exitCode, err := runScalewayRootStorageCommand(inst, `df -B1 --output=size / | tail -n 1 | tr -d '[:space:]'`, 20*time.Second)
	if err != nil {
		return fmt.Errorf("verify root filesystem size: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("verify root filesystem size: command exited %d: %s", exitCode, strings.TrimSpace(output))
	}
	actualBytes, err := strconv.ParseInt(strings.TrimSpace(output), 10, 64)
	if err != nil {
		return fmt.Errorf("verify root filesystem size: invalid byte count %q", strings.TrimSpace(output))
	}
	minimumBytes := int64(requestedSizeGB) * 1_000_000_000 * 9 / 10
	if actualBytes < minimumBytes {
		return fmt.Errorf("root filesystem is only %.1f GB after requesting %d GB boot storage", float64(actualBytes)/1_000_000_000, requestedSizeGB)
	}
	return nil
}
