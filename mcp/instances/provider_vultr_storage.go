package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type vultrBootBlock struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Region   string `json:"region"`
	Size     int    `json:"size_gb"`
	Attached string `json:"attached_to_instance"`
	Bootable bool   `json:"bootable"`
	Type     string `json:"block_type"`
}

func vultrBootRequest(i *Instance) (*BootStorageRequest, error) {
	if i.Provider != "vultr" {
		return nil, nil
	}
	request, e := bootStorageRequestFromInstance(i)
	if e != nil {
		return nil, e
	}
	if request == nil || request.StorageClass != "block" {
		return nil, nil
	}
	return request, nil
}
func reconcileVultrBootStorage(ctx *sdk.AppCtx, i *Instance) error {
	request, e := vultrBootRequest(i)
	if e != nil || request == nil {
		return e
	}
	volumes, e := dbListVolumes(ctx.AppDB(), i.ID, "")
	if e != nil {
		return e
	}
	for _, v := range volumes {
		if v.Role == "boot" {
			return nil
		}
	}
	meta := vultrMeta(i.ProviderMetadataJSON)
	if meta.BootLabel == "" {
		return errors.New("Vultr boot ownership label missing; inventory retained")
	}
	bound, e := storageBinding(ctx, "vultr", i.ProviderConnectionID)
	if e != nil {
		return e
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		data, e := executePagedProviderTool(ctx, bound.ConnectionID, "vultr", "block_storage_list", map[string]any{"per_page": 500})
		if e != nil {
			return e
		}
		var response struct {
			Blocks []vultrBootBlock `json:"blocks"`
		}
		if e = json.Unmarshal(data, &response); e != nil {
			return e
		}
		var found *vultrBootBlock
		for n := range response.Blocks {
			b := &response.Blocks[n]
			if b.Label != meta.BootLabel {
				continue
			}
			if found != nil {
				return errors.New("ambiguous Vultr boot ownership label")
			}
			found = b
		}
		if found != nil && found.Attached == i.ProviderID {
			if !found.Bootable || found.Region != i.Region || found.Type != "high_perf" || found.Size < request.SizeGB || found.ID == "" {
				return errors.New("Vultr boot block does not match the owned boot request")
			}
			return persistManagedBootVolume(ctx.AppDB(), i, &scalewayBootVolume{ID: found.ID, ProviderType: found.Type, StorageClass: "block", SizeGB: found.Size}, request)
		}
		if found != nil && found.Attached != "" && found.Attached != i.ProviderID {
			return errors.New("owned Vultr boot label attached to a different instance")
		}
		if i.Status == "destroying" || time.Now().After(deadline) {
			return errors.New("Vultr boot block identity not confirmed; inspect provider inventory before cleanup")
		}
		if e = sleepContext(instanceWorkerContext(ctx, i.ID), 2*time.Second); e != nil {
			return e
		}
	}
}

// Confirm the addressed compute resource is gone before releasing its persistent
// boot disk. Delete only recorded, app-owned boot IDs with matching immutable
// label, region and account; never infer ownership from arbitrary attachments.
func finalizeVultrBootStorage(ctx *sdk.AppCtx, i *Instance) error {
	request, e := vultrBootRequest(i)
	if e != nil || request == nil {
		return e
	}
	if _, e = storageBinding(ctx, "vultr", i.ProviderConnectionID); e != nil {
		return e
	}
	tool, args, e := apiProviderGetRequest(i)
	if e != nil {
		return e
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		res, e := ctx.PlatformAPI().ExecuteIntegrationTool(i.ProviderConnectionID, tool, args)
		if e != nil {
			return e
		}
		if res != nil && !res.Success && (upstreamStatus(res) == 404 || upstreamStatus(res) == 410) {
			break
		}
		if res == nil || !res.Success {
			return errors.New("cannot confirm Vultr instance deletion")
		}
		if time.Now().After(deadline) {
			return errors.New("Vultr instance deletion pending; boot block retained")
		}
		if e = sleepOperation(ctx, 2*time.Second); e != nil {
			return e
		}
	}
	volumes, e := dbListVolumes(ctx.AppDB(), i.ID, "")
	if e != nil {
		return e
	}
	meta := vultrMeta(i.ProviderMetadataJSON)
	bootCount := 0
	for _, v := range volumes {
		if v.Role == "boot" {
			bootCount++
		}
	}
	if bootCount > 1 {
		return errors.New("ambiguous Vultr boot inventory; refusing cleanup")
	}
	for _, v := range volumes {
		if v.Role != "boot" {
			continue
		}
		if !v.Managed || meta.BootLabel == "" {
			return errors.New("boot block ownership unverified")
		}
		deadline = time.Now().Add(90 * time.Second)
		for {
			res, e := ctx.PlatformAPI().ExecuteIntegrationTool(i.ProviderConnectionID, "block_storage_get", map[string]any{"block_id": v.ProviderVolumeID})
			if e != nil {
				return e
			}
			if res != nil && !res.Success && (upstreamStatus(res) == 404 || upstreamStatus(res) == 410) {
				if v.DeletePolicy == "retain" {
					return errors.New("retained Vultr boot block is missing; inventory preserved")
				}
				return completeVolumeOperation(ctx, v, "delete", volumeIntent{})
			}
			if res == nil || !res.Success {
				return errors.New("cannot verify owned Vultr boot block")
			}
			var root struct {
				Block vultrBootBlock `json:"block_storage"`
			}
			if e = json.Unmarshal(res.Data, &root); e != nil {
				return e
			}
			b := root.Block
			if b.ID != v.ProviderVolumeID || b.Label != meta.BootLabel || b.Region != i.Region || !b.Bootable {
				return fmt.Errorf("Vultr boot block %s identity changed; refusing cleanup", v.ProviderVolumeID)
			}
			if b.Attached == "" {
				if v.DeletePolicy == "retain" {
					return dbUpdateVolume(ctx.AppDB(), v.ID, map[string]any{"instance_id": nil, "status": "available"})
				}
				// The existing mutation journal recovers DELETE responses lost in transit.
				return deleteProviderVolume(ctx, v)
			}
			if b.Attached != i.ProviderID {
				return errors.New("Vultr boot block now belongs to another instance; refusing cleanup")
			}
			if time.Now().After(deadline) {
				return errors.New("Vultr boot block detach is pending")
			}
			if e = sleepOperation(ctx, 2*time.Second); e != nil {
				return e
			}
		}
	}
	return nil
}
