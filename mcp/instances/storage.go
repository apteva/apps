package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Storage role and class are deliberately orthogonal. A provider volume can,
// for example, be both role=boot and storage_class=block.
type StorageCapabilities struct {
	Provider             string        `json:"provider"`
	ConnectionID         int64         `json:"connection_id,omitempty"`
	BootSizeConfigurable bool          `json:"boot_size_configurable"`
	DataVolumes          bool          `json:"data_volumes"`
	DynamicAttach        bool          `json:"dynamic_attach"`
	Detach               bool          `json:"detach"`
	Resize               bool          `json:"resize"`
	Snapshots            bool          `json:"snapshots"`
	GuestPrepare         bool          `json:"guest_prepare"`
	GuestFilesystems     []string      `json:"guest_filesystems,omitempty"`
	StorageClasses       []string      `json:"storage_classes"`
	Tiers                []StorageTier `json:"tiers"`
	Notes                string        `json:"notes,omitempty"`
}

type StorageTier struct {
	Name          string `json:"name"`
	StorageClass  string `json:"storage_class,omitempty"`
	ProviderType  string `json:"provider_type,omitempty"`
	Technology    string `json:"technology,omitempty"`
	Attachment    string `json:"attachment,omitempty"`
	IOPS          int    `json:"iops,omitempty"`
	BandwidthMbps int    `json:"bandwidth_mbps,omitempty"`
	Persistent    bool   `json:"persistent"`
	Replication   string `json:"replication,omitempty"`
	Billing       string `json:"billing,omitempty"`
	Description   string `json:"description,omitempty"`
}

type BootStorageRequest struct {
	SizeGB       int    `json:"size_gb"`
	StorageClass string `json:"storage_class,omitempty"`
	Tier         string `json:"tier,omitempty"`
	ProviderType string `json:"provider_type,omitempty"`
	DeletePolicy string `json:"delete_policy,omitempty"`
}

type InstanceStorageRequest struct {
	Boot *BootStorageRequest `json:"boot,omitempty"`
}

type InstanceVolume struct {
	ID                   int64  `json:"id"`
	InstanceID           *int64 `json:"instance_id,omitempty"`
	Provider             string `json:"provider"`
	ProviderConnectionID int64  `json:"provider_connection_id"`
	ProviderVolumeID     string `json:"provider_volume_id"`
	Name                 string `json:"name"`
	Role                 string `json:"role"`
	StorageClass         string `json:"storage_class"`
	Tier                 string `json:"tier"`
	ProviderType         string `json:"provider_type,omitempty"`
	SizeGB               int    `json:"size_gb"`
	Region               string `json:"region,omitempty"`
	Status               string `json:"status"`
	Filesystem           string `json:"filesystem,omitempty"`
	MountPath            string `json:"mount_path,omitempty"`
	DevicePath           string `json:"device_path,omitempty"`
	GuestReady           bool   `json:"guest_ready"`
	Managed              bool   `json:"managed"`
	DeletePolicy         string `json:"delete_policy"`
	ProviderMetadataJSON string `json:"provider_metadata_json,omitempty"`
	ErrorMessage         string `json:"error,omitempty"`
	CreatedAt            string `json:"created_at,omitempty"`
	UpdatedAt            string `json:"updated_at,omitempty"`
}

type CreateVolumeInput struct {
	InstanceID           int64
	Provider             string
	ProviderConnectionID int64
	Name                 string
	SizeGB               int
	Region               string
	Tier                 string
	ProviderType         string
	DeletePolicy         string
	Prepare              *VolumePrepareRequest
}

var ErrVolumeNotFound = errors.New("volume not found")

func storageCapabilities(provider string) StorageCapabilities {
	provider = normalizeProvider(provider)
	cap := StorageCapabilities{Provider: provider}
	switch provider {
	case "hetzner":
		cap.DataVolumes, cap.DynamicAttach, cap.Detach, cap.Resize, cap.Snapshots = true, true, true, true, false
		cap.StorageClasses = []string{"block"}
		cap.Tiers = []StorageTier{{Name: "provider-default", Technology: "ssd", Attachment: "network", Persistent: true, Billing: "per_gb", Description: "Hetzner network Block Storage"}}
	case "digitalocean":
		cap.DataVolumes, cap.DynamicAttach, cap.Detach, cap.Resize, cap.Snapshots = true, true, true, true, true
		cap.StorageClasses = []string{"block"}
		cap.Tiers = []StorageTier{{Name: "provider-default", Technology: "ssd", Attachment: "network", Persistent: true, Billing: "per_gb", Description: "DigitalOcean Block Storage"}}
	case "vultr":
		cap.DataVolumes, cap.DynamicAttach, cap.Detach, cap.Resize = true, true, true, true
		cap.StorageClasses = []string{"block"}
		cap.Tiers = []StorageTier{{Name: "balanced", ProviderType: "storage_opt", Technology: "ssd", Attachment: "network", Persistent: true, Billing: "per_gb"}, {Name: "performance", ProviderType: "high_perf", Technology: "nvme", Attachment: "network", Persistent: true, Billing: "per_gb"}}
	case "aws-ec2":
		cap.BootSizeConfigurable, cap.DataVolumes, cap.DynamicAttach, cap.Detach, cap.Resize, cap.Snapshots = true, true, true, true, true, true
		cap.StorageClasses = []string{"block"}
		cap.Tiers = []StorageTier{{Name: "balanced", ProviderType: "gp3", Technology: "ssd", Attachment: "network", Persistent: true, Replication: "within availability zone", Billing: "per_gb_iops_throughput"}, {Name: "performance", ProviderType: "io2", Technology: "ssd", Attachment: "network", Persistent: true, Replication: "within availability zone", Billing: "per_gb_iops"}}
	case "scaleway":
		cap.BootSizeConfigurable, cap.DataVolumes, cap.DynamicAttach, cap.Detach, cap.Resize, cap.Snapshots = true, true, true, true, true, true
		cap.StorageClasses = []string{"local", "block"}
		cap.Tiers = []StorageTier{
			{Name: "local", StorageClass: "local", ProviderType: "l_ssd", Technology: "ssd", Attachment: "local", Persistent: false, Replication: "none", Billing: "included", Description: "Instance-local SSD where supported; tied to the physical or virtual server lifecycle"},
			{Name: "balanced", StorageClass: "block", ProviderType: "sbs_5k", Technology: "nvme", Attachment: "network", IOPS: 5000, Persistent: true, Replication: "3 replicas", Billing: "per_gb", Description: "Scaleway Block Storage 5K"},
			{Name: "performance", StorageClass: "block", ProviderType: "sbs_15k", Technology: "nvme", Attachment: "network", IOPS: 15000, Persistent: true, Replication: "3 replicas", Billing: "per_gb", Description: "Scaleway Block Storage 15K"},
		}
	case "huawei-cloud":
		cap.BootSizeConfigurable, cap.DataVolumes, cap.DynamicAttach, cap.Detach, cap.Resize, cap.Snapshots = true, true, true, true, true, true
		cap.StorageClasses = []string{"block"}
		cap.Tiers = []StorageTier{{Name: "balanced", ProviderType: "GPSSD", Technology: "ssd", Attachment: "network", Persistent: true, Billing: "per_gb"}, {Name: "performance", ProviderType: "SSD", Technology: "ssd", Attachment: "network", Persistent: true, Billing: "per_gb"}}
	case "linode":
		cap.DataVolumes, cap.DynamicAttach, cap.Detach, cap.Resize = true, true, true, true
		cap.StorageClasses = []string{"block"}
		cap.Tiers = []StorageTier{{Name: "provider-default", Technology: "ssd", Attachment: "network", Persistent: true, Billing: "per_gb", Description: "Akamai Cloud Block Storage"}}
	case "ovhcloud":
		cap.DataVolumes, cap.DynamicAttach, cap.Detach, cap.Resize, cap.Snapshots = true, true, true, true, true
		cap.StorageClasses = []string{"block"}
		cap.Tiers = []StorageTier{{Name: "balanced", ProviderType: "classic", Attachment: "network", Persistent: true, Billing: "per_gb"}, {Name: "performance", ProviderType: "high-speed", Technology: "ssd", Attachment: "network", Persistent: true, Billing: "per_gb"}}
	case "runpod":
		cap.BootSizeConfigurable, cap.DataVolumes, cap.Resize = true, true, true
		cap.StorageClasses = []string{"ephemeral", "network"}
		cap.Tiers = []StorageTier{{Name: "provider-default", ProviderType: "network", Attachment: "network", Persistent: true, Billing: "per_gb"}}
		cap.Notes = "Network volumes can only be attached when a Pod is created; an existing Pod must be recreated to change the network volume."
	case "contabo":
		cap.Notes = "Contabo's public API exposes Object Storage, not attachable block volumes; VPS disk size is fixed by the product."
	}
	for _, class := range cap.StorageClasses {
		if class == "block" {
			cap.GuestPrepare = true
			cap.GuestFilesystems = []string{"ext4", "xfs"}
			break
		}
	}
	return cap
}

func validateStorageRequest(provider string, req InstanceStorageRequest) error {
	if req.Boot == nil {
		return nil
	}
	if req.Boot.SizeGB <= 0 {
		return errors.New("storage.boot.size_gb must be positive")
	}
	cap := storageCapabilities(provider)
	if !cap.BootSizeConfigurable {
		return fmt.Errorf("provider %q does not support configurable boot storage through Instances; omit storage.boot to use the provider default", provider)
	}
	if req.Boot.DeletePolicy == "" {
		req.Boot.DeletePolicy = "with_instance"
	}
	if req.Boot.DeletePolicy != "with_instance" && req.Boot.DeletePolicy != "retain" {
		return errors.New("storage.boot.delete_policy must be with_instance or retain")
	}
	if req.Boot.Tier == "" {
		if req.Boot.StorageClass == "local" || req.Boot.ProviderType == "l_ssd" {
			req.Boot.Tier = "local"
		} else {
			req.Boot.Tier = "balanced"
		}
	}
	if req.Boot.StorageClass == "" {
		if req.Boot.ProviderType == "l_ssd" || req.Boot.Tier == "local" {
			req.Boot.StorageClass = "local"
		} else {
			req.Boot.StorageClass = "block"
		}
	}
	if !containsString(cap.StorageClasses, req.Boot.StorageClass) {
		return fmt.Errorf("provider %q does not support boot storage class %q", provider, req.Boot.StorageClass)
	}
	return nil
}

func storageBinding(ctx *sdk.AppCtx, provider string, connectionID int64) (*sdk.BoundIntegration, error) {
	provider = normalizeProvider(provider)
	if connectionID == 0 {
		return instanceProviderBinding(ctx, provider)
	}
	for _, bound := range ctx.IntegrationsFor("provider") {
		if bound != nil && bound.ConnectionID == connectionID && providerSlugForBinding(ctx, bound) == provider {
			return bound, nil
		}
	}
	return nil, fmt.Errorf("connection %d is not a bound %s provider connection", connectionID, provider)
}

func scanVolume(s rowScanner) (*InstanceVolume, error) {
	var v InstanceVolume
	var instanceID sql.NullInt64
	var managed int
	err := s.Scan(&v.ID, &instanceID, &v.Provider, &v.ProviderConnectionID, &v.ProviderVolumeID, &v.Name,
		&v.Role, &v.StorageClass, &v.Tier, &v.ProviderType, &v.SizeGB, &v.Region, &v.Status,
		&v.Filesystem, &v.MountPath, &v.DevicePath, &managed, &v.DeletePolicy, &v.ProviderMetadataJSON,
		&v.ErrorMessage, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if instanceID.Valid {
		v.InstanceID = &instanceID.Int64
	}
	v.Managed = managed != 0
	v.GuestReady = v.Filesystem != "" && v.MountPath != "" && v.DevicePath != ""
	return &v, nil
}

const volumeCols = `id, instance_id, provider, provider_connection_id, provider_volume_id, name,
	role, storage_class, tier, provider_type, size_gb, region, status, filesystem, mount_path,
	device_path, managed, delete_policy, provider_metadata_json, error_message,
	COALESCE(created_at,''), COALESCE(updated_at,'')`

func dbGetVolume(db *sql.DB, id int64) (*InstanceVolume, error) {
	v, err := scanVolume(db.QueryRow(`SELECT `+volumeCols+` FROM instance_volumes WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrVolumeNotFound
	}
	return v, err
}

func dbListVolumes(db *sql.DB, instanceID int64, provider string) ([]*InstanceVolume, error) {
	query := `SELECT ` + volumeCols + ` FROM instance_volumes WHERE 1=1`
	args := []any{}
	if instanceID > 0 {
		query += ` AND instance_id=?`
		args = append(args, instanceID)
	}
	if provider != "" {
		query += ` AND provider=?`
		args = append(args, normalizeProvider(provider))
	}
	query += ` ORDER BY id`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*InstanceVolume{}
	for rows.Next() {
		v, scanErr := scanVolume(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func dbCreateVolume(db *sql.DB, in CreateVolumeInput, providerID, storageClass, status, metadata string) (*InstanceVolume, error) {
	var instanceID any
	if in.InstanceID > 0 {
		instanceID = in.InstanceID
	}
	res, err := db.Exec(`INSERT INTO instance_volumes
		(instance_id,provider,provider_connection_id,provider_volume_id,name,role,storage_class,tier,provider_type,size_gb,region,status,managed,delete_policy,provider_metadata_json,created_at,updated_at)
		VALUES (?,?,?,?,?,'data',?,?,?,?,?,?,1,?,?,?,?)`,
		instanceID, in.Provider, in.ProviderConnectionID, providerID, in.Name, storageClass, in.Tier, in.ProviderType,
		in.SizeGB, in.Region, status, in.DeletePolicy, nullStr(metadata, "{}"), nowUTC(), nowUTC())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return dbGetVolume(db, id)
}

func dbUpdateVolume(db *sql.DB, id int64, fields map[string]any) error {
	cols, args := []string{}, []any{}
	for _, key := range []string{"instance_id", "size_gb", "status", "filesystem", "mount_path", "device_path", "delete_policy", "provider_metadata_json", "error_message"} {
		if value, ok := fields[key]; ok {
			cols, args = append(cols, key+"=?"), append(args, value)
		}
	}
	if len(cols) == 0 {
		return nil
	}
	cols = append(cols, "updated_at=?")
	args = append(args, nowUTC(), id)
	_, err := db.Exec(`UPDATE instance_volumes SET `+strings.Join(cols, ",")+` WHERE id=?`, args...)
	return err
}

func executeVolumeTool(ctx *sdk.AppCtx, connectionID int64, provider, tool string, args map[string]any) (json.RawMessage, error) {
	bound, err := storageBinding(ctx, provider, connectionID)
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

func providerTypeForTier(provider, tier, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if tier == "" {
		tier = "provider-default"
	}
	for _, candidate := range storageCapabilities(provider).Tiers {
		if candidate.Name == tier {
			return candidate.ProviderType
		}
	}
	return ""
}

func createProviderVolume(ctx *sdk.AppCtx, in CreateVolumeInput, inst *Instance) (json.RawMessage, string, string, error) {
	providerType := providerTypeForTier(in.Provider, in.Tier, in.ProviderType)
	tool, args, class := "", map[string]any{}, "block"
	switch in.Provider {
	case "hetzner":
		tool, args = "volume_create", map[string]any{"name": in.Name, "size": in.SizeGB, "location": in.Region, "labels": map[string]string{"managed-by": "apteva-instances"}}
	case "digitalocean":
		tool, args = "volume_create", map[string]any{"name": in.Name, "size_gigabytes": in.SizeGB, "region": in.Region, "description": "Managed by Apteva Instances", "tags": []string{"apteva-instances"}}
	case "vultr":
		tool, args = "block_storage_create", map[string]any{"label": in.Name, "size_gb": in.SizeGB, "region": in.Region, "block_type": providerType}
	case "aws-ec2":
		tool, args = "volume_create", map[string]any{"Action": "CreateVolume", "Version": "2016-11-15", "AvailabilityZone": in.Region, "Size": in.SizeGB, "VolumeType": providerType, "TagSpecification.1.ResourceType": "volume", "TagSpecification.1.Tag.1.Key": "Name", "TagSpecification.1.Tag.1.Value": in.Name}
	case "scaleway":
		project, err := scalewayDefaultProjectForConnection(ctx, in.ProviderConnectionID)
		if err != nil {
			return nil, "", class, err
		}
		iops := 5000
		if providerType == "sbs_15k" {
			iops = 15000
		}
		tool, args = "volume_create", map[string]any{"zone": in.Region, "project_id": project, "name": in.Name, "perf_iops": iops, "from_empty": map[string]any{"size": int64(in.SizeGB) * 1_000_000_000}, "tags": []string{"managed-by=apteva-instances"}}
	case "huawei-cloud":
		tool, args = "create_volume", map[string]any{"volume": map[string]any{"availability_zone": in.Region, "size": in.SizeGB, "volume_type": providerType, "name": in.Name, "description": "Managed by Apteva Instances"}}
	case "linode":
		args = map[string]any{"label": in.Name, "size": in.SizeGB, "region": in.Region, "tags": []string{"apteva-instances"}}
		if inst != nil {
			if id, err := strconv.Atoi(inst.ProviderID); err == nil {
				args["linode_id"] = id
			}
		}
		tool = "create_volume"
	case "ovhcloud":
		tool, args = "create_volume", map[string]any{"name": in.Name, "size": in.SizeGB, "region": in.Region, "type": providerType, "description": "Managed by Apteva Instances"}
	case "runpod":
		class = "network"
		tool, args = "create_network_volume", map[string]any{"name": in.Name, "size": in.SizeGB, "dataCenterId": in.Region}
	default:
		return nil, "", class, fmt.Errorf("provider %q does not support managed data volumes", in.Provider)
	}
	data, err := executeVolumeTool(ctx, in.ProviderConnectionID, in.Provider, tool, args)
	return data, providerType, class, err
}

func providerVolumeID(provider string, data json.RawMessage) string {
	switch provider {
	case "aws-ec2":
		return findJSONScalar(data, "volumeId")
	case "huawei-cloud":
		if id := findJSONScalar(data, "volume_id"); id != "" {
			return id
		}
	}
	return findJSONScalar(data, "id")
}

func resolveCreatedVolumeID(ctx *sdk.AppCtx, in CreateVolumeInput, data json.RawMessage) (string, error) {
	if id := providerVolumeID(in.Provider, data); id != "" {
		return id, nil
	}
	if in.Provider != "huawei-cloud" {
		return "", nil
	}
	jobID := findJSONScalar(data, "job_id")
	if jobID == "" {
		return "", nil
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		job, err := executeVolumeTool(ctx, in.ProviderConnectionID, in.Provider, "get_job", map[string]any{"job_id": jobID})
		if err != nil {
			return "", err
		}
		status := strings.ToUpper(findJSONScalar(job, "status"))
		if status == "SUCCESS" {
			if id := findJSONScalar(job, "volume_id"); id != "" {
				return id, nil
			}
			return "", errors.New("Huawei EVS create job succeeded without volume_id")
		}
		if status == "FAIL" || status == "FAILED" {
			return "", fmt.Errorf("Huawei EVS create job failed: %s", findJSONScalar(job, "fail_reason"))
		}
		if time.Now().After(deadline) {
			return "", errors.New("timed out waiting for Huawei EVS volume creation")
		}
		time.Sleep(5 * time.Second)
	}
}

func attachProviderVolume(ctx *sdk.AppCtx, v *InstanceVolume, inst *Instance) error {
	if inst == nil || inst.ProviderID == "" {
		return errors.New("target instance has no provider id")
	}
	var tool string
	args := map[string]any{}
	switch v.Provider {
	case "hetzner":
		tool, args = "volume_attach", map[string]any{"id": atoiAny(v.ProviderVolumeID), "server": atoiAny(inst.ProviderID), "automount": false}
	case "digitalocean":
		tool, args = "volume_action", map[string]any{"volume_id": v.ProviderVolumeID, "type": "attach", "droplet_id": atoiAny(inst.ProviderID), "region": v.Region}
	case "vultr":
		tool, args = "block_storage_attach", map[string]any{"block_id": v.ProviderVolumeID, "instance_id": inst.ProviderID, "live": true}
	case "aws-ec2":
		device := v.DevicePath
		if device == "" {
			device = nextAWSVolumeDevice(ctx.AppDB(), inst.ID)
			_ = dbUpdateVolume(ctx.AppDB(), v.ID, map[string]any{"device_path": device})
		}
		tool, args = "volume_attach", map[string]any{"Action": "AttachVolume", "Version": "2016-11-15", "VolumeId": v.ProviderVolumeID, "InstanceId": inst.ProviderID, "Device": "/dev/sdf"}
		args["Device"] = device
	case "scaleway":
		tool, args = "server_volume_attach", map[string]any{"zone": v.Region, "server_id": inst.ProviderID, "volume_id": v.ProviderVolumeID, "volume_type": "sbs_volume"}
	case "huawei-cloud":
		tool, args = "attach_volume", map[string]any{"server_id": inst.ProviderID, "volumeAttachment": map[string]any{"volumeId": v.ProviderVolumeID}}
	case "linode":
		return nil // attached atomically by create_volume when instance_id is supplied
	case "ovhcloud":
		tool, args = "attach_volume", map[string]any{"volumeId": v.ProviderVolumeID, "instanceId": inst.ProviderID}
	case "runpod":
		return errors.New(storageCapabilities("runpod").Notes)
	default:
		return providerAdapterUnavailable(v.Provider, "volume attach")
	}
	_, err := executeVolumeTool(ctx, v.ProviderConnectionID, v.Provider, tool, args)
	return err
}

func nextAWSVolumeDevice(db *sql.DB, instanceID int64) string {
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM instance_volumes WHERE instance_id=? AND provider='aws-ec2'`, instanceID).Scan(&count)
	// /dev/sdf through /dev/sdp are valid recommended data-device names.
	index := count - 1
	if index < 0 {
		index = 0
	}
	if index > 10 {
		index = 10
	}
	return "/dev/sd" + string(rune('f'+index))
}

func detachProviderVolume(ctx *sdk.AppCtx, v *InstanceVolume) error {
	if v.InstanceID == nil {
		return nil
	}
	inst, err := dbGetInstance(ctx.AppDB(), *v.InstanceID)
	if err != nil {
		return err
	}
	var tool string
	args := map[string]any{}
	switch v.Provider {
	case "hetzner":
		tool, args = "volume_detach", map[string]any{"id": atoiAny(v.ProviderVolumeID)}
	case "digitalocean":
		tool, args = "volume_action", map[string]any{"volume_id": v.ProviderVolumeID, "type": "detach", "droplet_id": atoiAny(inst.ProviderID), "region": v.Region}
	case "vultr":
		tool, args = "block_storage_detach", map[string]any{"block_id": v.ProviderVolumeID, "live": true}
	case "aws-ec2":
		tool, args = "volume_detach", map[string]any{"Action": "DetachVolume", "Version": "2016-11-15", "VolumeId": v.ProviderVolumeID, "InstanceId": inst.ProviderID}
	case "scaleway":
		tool, args = "server_volume_detach", map[string]any{"zone": v.Region, "server_id": inst.ProviderID, "volume_id": v.ProviderVolumeID}
	case "huawei-cloud":
		tool, args = "detach_volume", map[string]any{"server_id": inst.ProviderID, "attachment_id": v.ProviderVolumeID}
	case "linode":
		tool, args = "detach_volume", map[string]any{"volumeId": atoiAny(v.ProviderVolumeID)}
	case "ovhcloud":
		tool, args = "detach_volume", map[string]any{"volumeId": v.ProviderVolumeID, "instanceId": inst.ProviderID}
	default:
		return providerAdapterUnavailable(v.Provider, "volume detach")
	}
	_, err = executeVolumeTool(ctx, v.ProviderConnectionID, v.Provider, tool, args)
	return err
}

func resizeProviderVolume(ctx *sdk.AppCtx, v *InstanceVolume, sizeGB int) error {
	var tool string
	args := map[string]any{}
	switch v.Provider {
	case "hetzner":
		tool, args = "volume_resize", map[string]any{"id": atoiAny(v.ProviderVolumeID), "size": sizeGB}
	case "digitalocean":
		tool, args = "volume_action", map[string]any{"volume_id": v.ProviderVolumeID, "type": "resize", "size_gigabytes": sizeGB, "region": v.Region}
	case "vultr":
		tool, args = "block_storage_update", map[string]any{"block_id": v.ProviderVolumeID, "size_gb": sizeGB}
	case "aws-ec2":
		tool, args = "volume_resize", map[string]any{"Action": "ModifyVolume", "Version": "2016-11-15", "VolumeId": v.ProviderVolumeID, "Size": sizeGB}
	case "scaleway":
		tool, args = "volume_update", map[string]any{"zone": v.Region, "volume_id": v.ProviderVolumeID, "size": int64(sizeGB) * 1_000_000_000}
	case "huawei-cloud":
		tool, args = "resize_volume", map[string]any{"volume_id": v.ProviderVolumeID, "os-extend": map[string]any{"new_size": sizeGB}}
	case "linode":
		tool, args = "resize_volume", map[string]any{"volumeId": atoiAny(v.ProviderVolumeID), "size": sizeGB}
	case "ovhcloud":
		tool, args = "resize_volume", map[string]any{"volumeId": v.ProviderVolumeID, "size": sizeGB}
	case "runpod":
		tool, args = "update_network_volume", map[string]any{"networkVolumeId": v.ProviderVolumeID, "size": sizeGB}
	default:
		return providerAdapterUnavailable(v.Provider, "volume resize")
	}
	_, err := executeVolumeTool(ctx, v.ProviderConnectionID, v.Provider, tool, args)
	return err
}

func deleteProviderVolume(ctx *sdk.AppCtx, v *InstanceVolume) error {
	var tool string
	args := map[string]any{}
	switch v.Provider {
	case "hetzner":
		tool, args = "volume_delete", map[string]any{"id": atoiAny(v.ProviderVolumeID)}
	case "digitalocean":
		tool, args = "volume_delete", map[string]any{"volume_id": v.ProviderVolumeID}
	case "vultr":
		tool, args = "block_storage_delete", map[string]any{"block_id": v.ProviderVolumeID}
	case "aws-ec2":
		tool, args = "volume_delete", map[string]any{"Action": "DeleteVolume", "Version": "2016-11-15", "VolumeId": v.ProviderVolumeID}
	case "scaleway":
		tool = "volume_delete"
		if v.ProviderType == "l_ssd" || v.StorageClass == "local" {
			tool = "instance_volume_delete"
		}
		args = map[string]any{"zone": v.Region, "volume_id": v.ProviderVolumeID}
	case "huawei-cloud":
		tool, args = "delete_volume", map[string]any{"volume_id": v.ProviderVolumeID}
	case "linode":
		tool, args = "delete_volume", map[string]any{"volumeId": atoiAny(v.ProviderVolumeID)}
	case "ovhcloud":
		tool, args = "delete_volume", map[string]any{"volumeId": v.ProviderVolumeID}
	case "runpod":
		tool, args = "delete_network_volume", map[string]any{"networkVolumeId": v.ProviderVolumeID}
	default:
		return providerAdapterUnavailable(v.Provider, "volume delete")
	}
	_, err := executeVolumeTool(ctx, v.ProviderConnectionID, v.Provider, tool, args)
	return err
}

func prepareVolumesForInstanceDestroy(ctx *sdk.AppCtx, instanceID int64, force bool) error {
	volumes, err := dbListVolumes(ctx.AppDB(), instanceID, "")
	if err != nil {
		return err
	}
	for _, v := range volumes {
		if v.Role == "boot" {
			continue
		}
		if err := unmountPreparedVolume(ctx, v); err != nil {
			if !force {
				return fmt.Errorf("unmount volume %d: %w (retry with force=true to continue with provider-side detach/delete while the guest is unreachable)", v.ID, err)
			}
			ctx.Logger().Warn("instances: guest unmount failed; force destroy continues with provider APIs", "volume_id", v.ID, "err", err)
		}
		if err := detachProviderVolume(ctx, v); err != nil {
			return fmt.Errorf("detach volume %d before instance destroy: %w", v.ID, err)
		}
		if v.DeletePolicy == "with_instance" && v.Managed {
			if err := deleteProviderVolume(ctx, v); err != nil {
				return fmt.Errorf("delete volume %d with instance: %w", v.ID, err)
			}
			if _, err := ctx.AppDB().Exec(`DELETE FROM instance_volumes WHERE id=?`, v.ID); err != nil {
				return err
			}
			continue
		}
		if err := dbUpdateVolume(ctx.AppDB(), v.ID, map[string]any{"instance_id": nil, "status": "available", "mount_path": "", "device_path": ""}); err != nil {
			return err
		}
	}
	return nil
}

func createManagedVolume(ctx *sdk.AppCtx, in CreateVolumeInput) (*InstanceVolume, error) {
	if in.Name == "" || in.SizeGB <= 0 {
		return nil, errors.New("name and positive size_gb are required")
	}
	if in.Prepare != nil {
		if err := validateVolumePrepareRequest(in.Prepare); err != nil {
			return nil, fmt.Errorf("prepare: %w", err)
		}
		if in.InstanceID <= 0 {
			return nil, errors.New("prepare requires instance_id so the new volume can be attached")
		}
	}
	var inst *Instance
	if in.InstanceID > 0 {
		var err error
		inst, err = dbGetInstance(ctx.AppDB(), in.InstanceID)
		if err != nil {
			return nil, err
		}
		if inst.IsLocal() {
			return nil, errors.New("provider volumes cannot attach to the local instance")
		}
		if in.Provider == "" {
			in.Provider = inst.Provider
		}
		if in.Provider != inst.Provider {
			return nil, errors.New("volume and instance providers must match")
		}
		if in.Region == "" {
			in.Region = inst.Region
		}
		if in.ProviderConnectionID == 0 {
			in.ProviderConnectionID = inst.ProviderConnectionID
		}
		if inst.ProviderConnectionID > 0 && in.ProviderConnectionID != inst.ProviderConnectionID {
			return nil, errors.New("volume and instance must use the same provider connection")
		}
	}
	provider, err := resolveInstanceProvider(ctx, in.Provider)
	if err != nil {
		return nil, err
	}
	in.Provider = provider
	cap := storageCapabilities(provider)
	if !cap.DataVolumes {
		return nil, fmt.Errorf("provider %q does not support attachable data volumes: %s", provider, cap.Notes)
	}
	if inst != nil && !cap.DynamicAttach && provider != "linode" {
		return nil, fmt.Errorf("provider %q cannot attach a volume to an existing instance: %s", provider, cap.Notes)
	}
	bound, err := storageBinding(ctx, provider, in.ProviderConnectionID)
	if err != nil {
		return nil, err
	}
	in.ProviderConnectionID = bound.ConnectionID
	if in.Region == "" {
		return nil, errors.New("region is required when creating a standalone volume")
	}
	if in.Tier == "" {
		in.Tier = "provider-default"
		if len(cap.Tiers) > 0 {
			in.Tier = cap.Tiers[0].Name
		}
	}
	if in.DeletePolicy == "" {
		in.DeletePolicy = "retain"
	}
	if in.DeletePolicy != "retain" && in.DeletePolicy != "with_instance" {
		return nil, errors.New("delete_policy must be retain or with_instance")
	}
	data, providerType, class, err := createProviderVolume(ctx, in, inst)
	if err != nil {
		return nil, err
	}
	in.ProviderType = providerType
	providerID, err := resolveCreatedVolumeID(ctx, in, data)
	if err != nil {
		return nil, err
	}
	if providerID == "" {
		return nil, fmt.Errorf("%s volume create response did not include a volume id", provider)
	}
	metadata, _ := json.Marshal(map[string]any{"create_response": json.RawMessage(data)})
	v, err := dbCreateVolume(ctx.AppDB(), in, providerID, class, "available", string(metadata))
	if err != nil {
		return nil, fmt.Errorf("volume %s was created upstream but could not be recorded: %w", providerID, err)
	}
	if inst != nil {
		if err := attachProviderVolume(ctx, v, inst); err != nil {
			_ = dbUpdateVolume(ctx.AppDB(), v.ID, map[string]any{"status": "error", "error_message": err.Error()})
			return nil, fmt.Errorf("volume %d was created and recorded but attachment failed: %w", v.ID, err)
		}
		_ = dbUpdateVolume(ctx.AppDB(), v.ID, map[string]any{"status": "attached", "instance_id": inst.ID, "error_message": ""})
		v, err = dbGetVolume(ctx.AppDB(), v.ID)
		if err != nil {
			return nil, err
		}
		if in.Prepare != nil {
			prepared, _, prepareErr := prepareAttachedVolume(ctx, v, in.Prepare)
			if prepareErr != nil {
				return nil, prepareErr
			}
			return prepared, nil
		}
		return v, nil
	}
	return v, nil
}

func atoiAny(s string) any {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return s
	}
	return n
}

func (a *App) toolStorageCapabilities(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	provider, err := resolveInstanceProvider(ctx, strArg(args, "provider"))
	if err != nil {
		return nil, err
	}
	bound, err := storageBinding(ctx, provider, int64Arg(args, "provider_connection_id"))
	if err != nil {
		return nil, err
	}
	cap := storageCapabilities(provider)
	cap.ConnectionID = bound.ConnectionID
	return map[string]any{"capabilities": cap}, nil
}

func (a *App) toolListStorageTypes(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	result, err := a.toolStorageCapabilities(ctx, args)
	if err != nil {
		return nil, err
	}
	cap := result.(map[string]any)["capabilities"].(StorageCapabilities)
	return map[string]any{"provider": cap.Provider, "storage_classes": cap.StorageClasses, "tiers": cap.Tiers}, nil
}

func (a *App) toolVolumeCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	prepare, err := volumePrepareRequestArg(args, "prepare", true)
	if err != nil {
		return nil, err
	}
	v, err := createManagedVolume(ctx, CreateVolumeInput{
		InstanceID: int64Arg(args, "instance_id"), Provider: strArg(args, "provider"), ProviderConnectionID: int64Arg(args, "provider_connection_id"),
		Name: strArg(args, "name"), SizeGB: intArg(args, "size_gb", 0), Region: strArg(args, "region"),
		Tier: strArg(args, "tier"), ProviderType: strArg(args, "provider_type"), DeletePolicy: strArg(args, "delete_policy"), Prepare: prepare,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"volume": v}, nil
}

func (a *App) toolVolumeGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	v, err := dbGetVolume(ctx.AppDB(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"volume": v}, nil
}

func (a *App) toolVolumeList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	volumes, err := dbListVolumes(ctx.AppDB(), int64Arg(args, "instance_id"), strArg(args, "provider"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"volumes": volumes, "count": len(volumes)}, nil
}

func (a *App) toolVolumeAttach(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	prepare, err := volumePrepareRequestArg(args, "prepare", false)
	if err != nil {
		return nil, err
	}
	v, err := dbGetVolume(ctx.AppDB(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	inst, err := dbGetInstance(ctx.AppDB(), int64Arg(args, "instance_id"))
	if err != nil {
		return nil, err
	}
	if v.InstanceID != nil {
		return nil, errors.New("volume is already attached")
	}
	if v.Provider != inst.Provider {
		return nil, errors.New("volume and instance providers must match")
	}
	if v.ProviderConnectionID != inst.ProviderConnectionID {
		return nil, errors.New("volume and instance must use the same provider connection")
	}
	if v.Region != "" && inst.Region != "" && v.Region != inst.Region {
		return nil, fmt.Errorf("volume region %q does not match instance region %q", v.Region, inst.Region)
	}
	if err = attachProviderVolume(ctx, v, inst); err != nil {
		return nil, err
	}
	_ = dbUpdateVolume(ctx.AppDB(), v.ID, map[string]any{"instance_id": inst.ID, "status": "attached", "error_message": ""})
	v, _ = dbGetVolume(ctx.AppDB(), v.ID)
	if prepare != nil {
		prepared, result, prepareErr := prepareAttachedVolume(ctx, v, prepare)
		if prepareErr != nil {
			return nil, prepareErr
		}
		return map[string]any{"volume": prepared, "prepared": result}, nil
	}
	return map[string]any{"volume": v}, nil
}

func (a *App) toolVolumeDetach(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	v, err := dbGetVolume(ctx.AppDB(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	if v.Role == "boot" {
		return nil, errors.New("boot volumes cannot be detached through instance_volume_detach")
	}
	if err = unmountPreparedVolume(ctx, v); err != nil {
		return nil, err
	}
	if err = detachProviderVolume(ctx, v); err != nil {
		return nil, err
	}
	_ = dbUpdateVolume(ctx.AppDB(), v.ID, map[string]any{"instance_id": nil, "status": "available", "mount_path": "", "device_path": "", "error_message": ""})
	v, _ = dbGetVolume(ctx.AppDB(), v.ID)
	return map[string]any{"volume": v}, nil
}

func (a *App) toolVolumeResize(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	v, err := dbGetVolume(ctx.AppDB(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	size := intArg(args, "size_gb", 0)
	if size <= v.SizeGB {
		return nil, fmt.Errorf("size_gb must be greater than current size %d; volumes cannot shrink", v.SizeGB)
	}
	if err = resizeProviderVolume(ctx, v, size); err != nil {
		return nil, err
	}
	_ = dbUpdateVolume(ctx.AppDB(), v.ID, map[string]any{"size_gb": size, "status": map[bool]string{true: "attached", false: "available"}[v.InstanceID != nil], "error_message": ""})
	v, _ = dbGetVolume(ctx.AppDB(), v.ID)
	return map[string]any{"volume": v, "filesystem_resize_required": v.Filesystem != ""}, nil
}

func (a *App) toolVolumeDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if !boolArg(args, "confirm", false) {
		return nil, errors.New("confirm=true is required to permanently delete a volume")
	}
	v, err := dbGetVolume(ctx.AppDB(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	if v.Role == "boot" {
		return nil, errors.New("boot volumes are deleted only with their instance")
	}
	if v.InstanceID != nil {
		return nil, errors.New("detach the volume before deleting it")
	}
	if !v.Managed {
		return nil, errors.New("external volumes cannot be deleted until explicitly adopted")
	}
	if err = deleteProviderVolume(ctx, v); err != nil {
		return nil, err
	}
	_, err = ctx.AppDB().Exec(`DELETE FROM instance_volumes WHERE id=?`, v.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "id": v.ID, "provider_volume_id": v.ProviderVolumeID}, nil
}
