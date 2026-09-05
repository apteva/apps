package main

import (
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// The installed AWS integration cannot modify DeleteOnTermination. Refuse an
// override it cannot honor, rather than silently deleting a requested disk.
func applyDestroyBootPolicy(ctx *sdk.AppCtx, inst *Instance, retain bool) error {
	var request InstanceStorageRequest
	if err := json.Unmarshal([]byte(inst.StorageJSON), &request); err != nil {
		return err
	}
	switch inst.Provider {
	case "aws-ec2":
		volumes, err := dbListVolumes(ctx.AppDB(), inst.ID, "")
		if err != nil {
			return err
		}
		for _, v := range volumes {
			if v.Role == "boot" {
				if (v.DeletePolicy == "retain") != retain {
					return fmt.Errorf("AWS root DeleteOnTermination does not match requested retention; change it in AWS and retry so Instances can verify it")
				}
				return nil
			}
		}
		return fmt.Errorf("AWS root disk policy is unverified")
	case "huawei-cloud", "runpod":
		if retain {
			return fmt.Errorf("%s does not support retaining boot storage through this adapter", inst.Provider)
		}
	case "scaleway":
		if request.Boot != nil {
			if retain {
				request.Boot.DeletePolicy = "retain"
			} else {
				request.Boot.DeletePolicy = "with_instance"
			}
			data, _ := json.Marshal(request)
			if err := dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"storage_json": string(data)}); err != nil {
				return err
			}
			inst.StorageJSON = string(data)
		}
	}
	return nil
}

// DescribeInstances is the authority for the root disk and its deletion flag.
func reconcileAWSBootStorage(ctx *sdk.AppCtx, inst *Instance) error {
	if inst.Provider != "aws-ec2" || inst.ProviderID == "" {
		return nil
	}
	tool, args, err := apiProviderGetRequest(inst)
	if err != nil {
		return err
	}
	data, err := executeProviderToolOnConnection(ctx, inst.ProviderConnectionID, inst.Provider, tool, args)
	if err != nil {
		if strings.Contains(err.Error(), "status=404") && inst.Status == "destroying" {
			return nil
		}
		return err
	}
	var root any
	if err = json.Unmarshal(data, &root); err != nil {
		return err
	}
	var server map[string]any
	for _, obj := range collectMaps(root) {
		if mapString(obj, "instanceId") == inst.ProviderID {
			server = obj
			break
		}
	}
	if server == nil {
		if inst.Status == "destroying" {
			volumes, e := dbListVolumes(ctx.AppDB(), inst.ID, "")
			if e == nil && len(volumes) > 0 {
				return nil
			}
		}
		return fmt.Errorf("AWS instance identity was not present in DescribeInstances")
	}
	rootDevice := mapString(server, "rootDeviceName")
	for _, mapping := range collectMaps(server["blockDeviceMapping"]) {
		if mapString(mapping, "deviceName") != rootDevice || rootDevice == "" {
			continue
		}
		ebs, ok := mapping["ebs"].(map[string]any)
		if !ok {
			continue
		}
		id := mapString(ebs, "volumeId")
		if id == "" {
			continue
		}
		policy := "retain"
		flag := fmt.Sprint(ebs["deleteOnTermination"])
		if flag == "true" {
			policy = "with_instance"
		} else if flag != "false" {
			return fmt.Errorf("AWS root DeleteOnTermination is unknown")
		}
		actual, err := readProviderVolume(ctx, &InstanceVolume{Provider: inst.Provider, ProviderConnectionID: inst.ProviderConnectionID, ProviderVolumeID: id, Region: inst.Region})
		if err != nil {
			return err
		}
		_, err = ctx.AppDB().Exec(`INSERT INTO instance_volumes(instance_id,provider,provider_connection_id,provider_volume_id,name,role,storage_class,tier,provider_type,size_gb,region,status,managed,delete_policy,provider_device_path)
   VALUES(?,?,?,?,?,'boot','block','provider-default','ebs',?,?,'attached',1,?,?) ON CONFLICT(provider_connection_id,provider,provider_volume_id) DO UPDATE SET delete_policy=excluded.delete_policy,size_gb=excluded.size_gb,provider_device_path=excluded.provider_device_path`, inst.ID, inst.Provider, inst.ProviderConnectionID, id, inst.Name+"-boot", actual.SizeGB, inst.Region, policy, rootDevice)
		return err
	}
	return fmt.Errorf("AWS root disk mapping is unavailable; deletion policy cannot be verified")
}

func finalizeAWSBootVolumes(ctx *sdk.AppCtx, inst *Instance) error {
	if inst.Provider != "aws-ec2" {
		return nil
	}
	volumes, err := dbListVolumes(ctx.AppDB(), inst.ID, "")
	if err != nil {
		return err
	}
	for _, v := range volumes {
		if v.Role != "boot" {
			continue
		}
		operation := "delete"
		if v.DeletePolicy == "retain" {
			operation = "detach"
		}
		if err = verifyVolumeOperation(ctx, v, operation, volumeIntent{}); err != nil {
			return err
		}
	}
	return nil
}
