package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "instance_list_providers",
			Description: "List all VPS provider connections bound to this Instances install and identify the configured default. Read-only and does not call a provider API.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolListProviders,
		},
		{
			Name: "instance_create",
			Description: "Provision a new instance via a bound VPS provider. Compatible provider bindings: hetzner, digitalocean, contabo, vultr, aws-ec2, scaleway, huawei-cloud, linode, ovhcloud, runpod. " +
				"Args: name (req), provider? (defaults to the configured default binding), region?, size?, image?, tags_json?. " +
				"Local instance (id 0) is auto-seeded; passing provider=local is refused.",
			InputSchema: schemaObject(map[string]any{
				"name":                   map[string]any{"type": "string"},
				"provider":               map[string]any{"type": "string"},
				"provider_connection_id": map[string]any{"type": "integer", "description": "Choose a specific bound account when more than one connection exists for the provider"},
				"region":                 map[string]any{"type": "string"},
				"size":                   map[string]any{"type": "string"},
				"image":                  map[string]any{"type": "string"},
				"tags_json":              map[string]any{"type": "string"},
				"storage": map[string]any{
					"type":        "object",
					"description": "Optional provider-neutral boot storage. Omit to preserve the provider/image default.",
					"properties": map[string]any{
						"boot": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"size_gb":       map[string]any{"type": "integer", "minimum": 1},
								"tier":          map[string]any{"type": "string", "enum": []string{"provider-default", "balanced", "performance"}},
								"provider_type": map[string]any{"type": "string"},
								"delete_policy": map[string]any{"type": "string", "enum": []string{"with_instance", "retain"}},
							},
							"required": []string{"size_gb"},
						},
					},
				},
			}, []string{"name"}),
			Handler: a.toolCreate,
		},
		{
			Name:        "instance_storage_capabilities",
			Description: "Describe boot/data storage support for a bound provider. Returns separate boot_size_configurable, data_volumes, dynamic_attach, detach, resize, snapshots, storage_classes, and provider tier mappings.",
			InputSchema: schemaObject(map[string]any{"provider": map[string]any{"type": "string"}, "provider_connection_id": map[string]any{"type": "integer"}}, nil),
			Handler:     a.toolStorageCapabilities,
		},
		{
			Name:        "instance_list_storage_types",
			Description: "List provider-neutral storage classes and tiers with their native provider type mappings.",
			InputSchema: schemaObject(map[string]any{"provider": map[string]any{"type": "string"}, "provider_connection_id": map[string]any{"type": "integer"}}, nil),
			Handler:     a.toolListStorageTypes,
		},
		{
			Name:        "instance_volume_create",
			Description: "Create a managed data volume and optionally attach it to an existing instance. Pass prepare to safely format a newly created blank volume, persist it by filesystem UUID, and mount it for immediate use. Data volumes default to delete_policy=retain.",
			InputSchema: schemaObject(map[string]any{
				"instance_id": map[string]any{"type": "integer"}, "provider": map[string]any{"type": "string"}, "provider_connection_id": map[string]any{"type": "integer"},
				"name": map[string]any{"type": "string"}, "size_gb": map[string]any{"type": "integer", "minimum": 1}, "region": map[string]any{"type": "string"},
				"tier": map[string]any{"type": "string", "enum": []string{"provider-default", "balanced", "performance"}}, "provider_type": map[string]any{"type": "string"},
				"delete_policy": map[string]any{"type": "string", "enum": []string{"retain", "with_instance"}},
				"prepare":       volumePrepareObjectSchema("Optional guest activation after attachment. For a newly created volume format_if_blank defaults to true."),
			}, []string{"name", "size_gb"}),
			Handler: a.toolVolumeCreate,
		},
		{Name: "instance_volume_get", Description: "Get one managed volume by Instances volume id.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolVolumeGet},
		{Name: "instance_volume_list", Description: "List volumes tracked by Instances. Optional filters: instance_id, provider.", InputSchema: schemaObject(map[string]any{"instance_id": map[string]any{"type": "integer"}, "provider": map[string]any{"type": "string"}}, nil), Handler: a.toolVolumeList},
		{Name: "instance_volume_attach", Description: "Attach an available managed volume to an existing instance in the same provider and region. Pass prepare to mount it automatically; formatting an existing blank volume requires format_if_blank=true.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "instance_id": map[string]any{"type": "integer"}, "prepare": volumePrepareObjectSchema("Optional guest activation after attachment. format_if_blank defaults to false for an existing volume.")}, []string{"id", "instance_id"}), Handler: a.toolVolumeAttach},
		{Name: "instance_volume_prepare", Description: "Make an attached Linux data block volume usable inside the guest over SSH. Discovers the stable device, refuses ambiguous devices and unknown signatures, optionally formats only when blank, writes a UUID-based fstab entry, mounts it, and verifies the result.", InputSchema: schemaObject(map[string]any{
			"id":              map[string]any{"type": "integer"},
			"filesystem":      map[string]any{"type": "string", "enum": []string{"ext4", "xfs"}},
			"mount_path":      map[string]any{"type": "string", "description": "Dedicated absolute path such as /srv/media"},
			"owner":           map[string]any{"type": "string", "description": "user:group or uid:gid; defaults to root:root"},
			"mode":            map[string]any{"type": "string", "description": "Octal mode; defaults to 0755"},
			"mount_options":   map[string]any{"type": "string", "description": "Comma-separated fstab options; defaults to defaults,nofail"},
			"format_if_blank": map[string]any{"type": "boolean", "description": "Required to create a filesystem on a blank existing volume; never overwrites a signature"},
		}, []string{"id", "mount_path"}), Handler: a.toolVolumePrepare},
		{Name: "instance_volume_detach", Description: "Detach a data volume without deleting it. Boot volumes are refused.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolVolumeDetach},
		{Name: "instance_volume_resize", Description: "Increase a managed volume size. Shrinking is refused. Filesystem growth remains a separate guest operation.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "size_gb": map[string]any{"type": "integer", "minimum": 1}}, []string{"id", "size_gb"}), Handler: a.toolVolumeResize},
		{Name: "instance_volume_delete", Description: "Permanently delete a detached, app-managed data volume. Requires confirm=true; external and boot volumes are refused.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "confirm": map[string]any{"type": "boolean"}}, []string{"id", "confirm"}), Handler: a.toolVolumeDelete},
		{
			Name:        "object_storage_list_providers",
			Description: "List bound provider accounts that can provision object storage through Instances.",
			InputSchema: schemaObject(map[string]any{}, nil), Handler: a.toolObjectStorageListProviders,
		},
		{
			Name:        "object_storage_list_plans",
			Description: "List object-storage regions/clusters and plans/tiers for a bound provider.",
			InputSchema: schemaObject(map[string]any{"provider": map[string]any{"type": "string"}, "provider_connection_id": map[string]any{"type": "integer"}}, nil), Handler: a.toolObjectStorageListPlans,
		},
		{
			Name:        "object_storage_create",
			Description: "Provision object storage and return its S3 credentials once. Instances stores resource metadata and access-key ID, never the secret and never an automatic Connection.",
			InputSchema: schemaObject(map[string]any{
				"name": map[string]any{"type": "string"}, "provider": map[string]any{"type": "string"}, "provider_connection_id": map[string]any{"type": "integer"},
				"region": map[string]any{"type": "string", "description": "Scaleway region or Vultr cluster ID"}, "plan": map[string]any{"type": "string", "description": "Provider tier ID; optional where supported"},
				"bucket": map[string]any{"type": "string", "description": "Optional Scaleway bucket name; a globally unique name is generated when omitted"},
			}, []string{"name"}), Handler: a.toolObjectStorageCreate,
		},
		{Name: "object_storage_get", Description: "Get one provisioned object-storage resource. Secrets are never persisted and are not returned here.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolObjectStorageGet},
		{Name: "object_storage_list", Description: "List object-storage resources tracked by Instances. Optional provider filter.", InputSchema: schemaObject(map[string]any{"provider": map[string]any{"type": "string"}}, nil), Handler: a.toolObjectStorageList},
		{Name: "object_storage_rotate_credentials", Description: "Rotate credentials and return the new secret once. The previous provider key is invalidated where supported.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolObjectStorageRotateCredentials},
		{Name: "object_storage_destroy", Description: "Delete a managed object-storage resource. Requires confirm=true. Scaleway buckets must be empty.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "confirm": map[string]any{"type": "boolean"}}, []string{"id", "confirm"}), Handler: a.toolObjectStorageDestroy},
		{
			Name:        "instance_register",
			Description: "Register an externally managed SSH host such as a Mac. Instances generates a dedicated keypair and returns the public key to add to the remote user's authorized_keys. The row remains provisioning until instance_wait_ready succeeds. Args: name, ssh_host, ssh_user, ssh_port?, tags_json?.",
			InputSchema: schemaObject(map[string]any{
				"name":      map[string]any{"type": "string"},
				"ssh_host":  map[string]any{"type": "string"},
				"ssh_user":  map[string]any{"type": "string"},
				"ssh_port":  map[string]any{"type": "integer"},
				"tags_json": map[string]any{"type": "string"},
			}, []string{"name", "ssh_host", "ssh_user"}),
			Handler: a.toolRegister,
		},
		{
			Name:        "instance_get",
			Description: "Fetch one instance by id (0 for local).",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolGet,
		},
		{
			Name:        "instance_list",
			Description: "List instances. Optional filters: provider, status.",
			InputSchema: schemaObject(map[string]any{
				"provider": map[string]any{"type": "string"},
				"status":   map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolList,
		},
		{
			Name:        "instance_destroy",
			Description: "Terminate the upstream resource and remove the row. Refused for local (id 0). Idempotent.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolDestroy,
		},
		{
			Name: "instance_upgrade",
			Description: "Change a remote instance to another server type in-place where the provider adapter supports it. Hetzner is implemented today. This shuts the server down, " +
				"changes server_type, powers it back on, and waits for SSH. Args: id, size, upgrade_disk? (default false).",
			InputSchema: schemaObject(map[string]any{
				"id":           map[string]any{"type": "integer"},
				"size":         map[string]any{"type": "string"},
				"upgrade_disk": map[string]any{"type": "boolean"},
			}, []string{"id", "size"}),
			Handler: a.toolUpgrade,
		},
		{
			Name: "instance_run_command",
			Description: "Execute a shell command on the instance. Local: in-process exec. Remote: SSH. " +
				"Output is stdout+stderr combined, capped at 1 MB. Args: id, cmd, timeout_s? (default 30).",
			InputSchema: schemaObject(map[string]any{
				"id":        map[string]any{"type": "integer"},
				"cmd":       map[string]any{"type": "string"},
				"timeout_s": map[string]any{"type": "integer"},
			}, []string{"id", "cmd"}),
			Handler: a.toolRunCommand,
		},
		{
			Name: "instance_upload_file",
			Description: "Write file content to the instance. Local: filesystem write under ctx.DataDir/local-files (path-allowlisted). " +
				"Remote: SCP-equivalent over SSH. Args: id, path, content_b64 (base64-encoded body).",
			InputSchema: schemaObject(map[string]any{
				"id":          map[string]any{"type": "integer"},
				"path":        map[string]any{"type": "string"},
				"content_b64": map[string]any{"type": "string"},
			}, []string{"id", "path", "content_b64"}),
			Handler: a.toolUploadFile,
		},
		{
			Name: "instance_download_file",
			Description: "Read file content from the instance. Local: filesystem read under ctx.DataDir/local-files (path-allowlisted). " +
				"Remote: SSH read. Returns content_b64 and byte count. Args: id, path.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"path": map[string]any{"type": "string"},
			}, []string{"id", "path"}),
			Handler: a.toolDownloadFile,
		},
		{
			Name: "instance_open_tunnel",
			Description: "Open or reuse a loopback-only TCP tunnel through a remote instance's SSH connection. " +
				"Returns local_host and local_port. Args: id, target_port.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"}, "target_port": map[string]any{"type": "integer"},
			}, []string{"id", "target_port"}),
			Handler: a.toolOpenTunnel,
		},
		{
			Name:        "instance_close_tunnel",
			Description: "Close a loopback TCP tunnel for an instance target port. Args: id, target_port.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"}, "target_port": map[string]any{"type": "integer"},
			}, []string{"id", "target_port"}),
			Handler: a.toolCloseTunnel,
		},
		{
			Name: "instance_wait_ready",
			Description: "Poll the instance until SSH accepts the key and can run a non-interactive command. Already 'ready' instances return immediately. " +
				"Args: id, timeout_s? (default 300).",
			InputSchema: schemaObject(map[string]any{
				"id":        map[string]any{"type": "integer"},
				"timeout_s": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolWaitReady,
		},
		{
			Name:        "instance_metrics",
			Description: "CPU / memory / disk / network / load / uptime. Local: gopsutil. Remote: SSH-execute /proc parse. Cached 5s.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolMetrics,
		},
		{
			Name: "instance_list_server_types",
			Description: "List the VPS server types (sizes) available from the bound provider, live from the upstream API. " +
				"Returns active, non-deprecated types only: name + cores + memory_gb + disk_gb + monthly/hourly price + available_in (locations). " +
				"Use this to discover valid `size` values for instance_create instead of hardcoding. " +
				"Args: provider? (default: bound provider).",
			InputSchema: schemaObject(map[string]any{"provider": map[string]any{"type": "string"}}, nil),
			Handler:     a.toolListServerTypes,
		},
		{
			Name: "instance_list_locations",
			Description: "List the VPS regions available from the bound provider, live from upstream. " +
				"Returns name + city + country + network_zone for each. " +
				"Use to discover valid `region` values for instance_create. Args: provider? (default: bound provider).",
			InputSchema: schemaObject(map[string]any{"provider": map[string]any{"type": "string"}}, nil),
			Handler:     a.toolListLocations,
		},
		{
			Name: "instance_list_images",
			Description: "List OS images available from the bound provider, live from upstream (system images only — snapshots/backups/apps excluded). " +
				"Returns name + os_flavor + os_version + architecture. " +
				"Use to discover valid `image` values for instance_create. Args: provider? (default: bound provider).",
			InputSchema: schemaObject(map[string]any{"provider": map[string]any{"type": "string"}}, nil),
			Handler:     a.toolListImages,
		},
	}
}

func volumePrepareObjectSchema(description string) map[string]any {
	return map[string]any{
		"type":        "object",
		"description": description,
		"properties": map[string]any{
			"filesystem":      map[string]any{"type": "string", "enum": []string{"ext4", "xfs"}},
			"mount_path":      map[string]any{"type": "string"},
			"owner":           map[string]any{"type": "string"},
			"mode":            map[string]any{"type": "string"},
			"mount_options":   map[string]any{"type": "string"},
			"format_if_blank": map[string]any{"type": "boolean"},
		},
		"required": []string{"mount_path"},
	}
}

// ─── Handlers ─────────────────────────────────────────────────────

func (a *App) toolListProviders(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	providers := boundInstanceProviders(ctx)
	defaultProvider := ""
	for _, provider := range providers {
		if provider.Default {
			defaultProvider = provider.Provider
			break
		}
	}
	return map[string]any{"providers": providers, "default": defaultProvider, "count": len(providers)}, nil
}

func (a *App) toolCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name := strArg(args, "name")
	if name == "" {
		return nil, errors.New("name required")
	}
	provider := strArg(args, "provider")
	in := CreateInstanceInput{
		Name: name, Provider: provider, ProviderConnectionID: int64Arg(args, "provider_connection_id"),
		Region: strArg(args, "region"), Size: strArg(args, "size"), Image: strArg(args, "image"), TagsJSON: strArg(args, "tags_json"),
		Storage: storageRequestArg(args),
	}
	inst, err := provisionInstance(ctx, in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"instance": inst.stripSecrets()}, nil
}

func storageRequestArg(args map[string]any) InstanceStorageRequest {
	storage, _ := args["storage"].(map[string]any)
	boot, _ := storage["boot"].(map[string]any)
	if len(boot) == 0 {
		return InstanceStorageRequest{}
	}
	return InstanceStorageRequest{Boot: &BootStorageRequest{
		SizeGB: intArg(boot, "size_gb", 0), Tier: strArg(boot, "tier"), ProviderType: strArg(boot, "provider_type"), DeletePolicy: strArg(boot, "delete_policy"),
	}}
}

func (a *App) toolRegister(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name := strings.TrimSpace(strArg(args, "name"))
	host := strings.TrimSpace(strArg(args, "ssh_host"))
	user := strings.TrimSpace(strArg(args, "ssh_user"))
	port := intArg(args, "ssh_port", 22)
	if name == "" || host == "" || user == "" {
		return nil, errors.New("name, ssh_host, and ssh_user are required")
	}
	if strings.ContainsAny(host, " /\\\t\r\n") {
		return nil, errors.New("ssh_host must be a hostname or IP address without whitespace")
	}
	if strings.ContainsAny(user, " /\\\t\r\n@:") {
		return nil, errors.New("ssh_user contains invalid characters")
	}
	if port <= 0 || port > 65535 {
		return nil, errors.New("ssh_port must be between 1 and 65535")
	}
	privateKey, publicKey, err := generateSSHKeypair()
	if err != nil {
		return nil, err
	}
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{
		Name: name, Provider: "external", ProviderID: host + ":" + fmt.Sprint(port),
		Status: "provisioning", SSHHost: host, SSHPort: port, SSHUser: user,
		SSHPrivateKey: privateKey, SSHPublicKey: publicKey,
		TagsJSON: strArg(args, "tags_json"),
	})
	if err != nil {
		return nil, err
	}
	emitInstanceCreated(ctx, inst)
	emitInstanceStatus(ctx, inst)
	return map[string]any{
		"instance": inst.stripSecrets(),
		"authorization": map[string]any{
			"ssh_user": user, "ssh_host": host, "ssh_port": port,
			"public_key": publicKey,
			"next_step":  "Add public_key as one line in the remote user's ~/.ssh/authorized_keys, then call instance_wait_ready with this instance id.",
		},
	}, nil
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	inst, err := dbGetInstance(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"instance": inst.stripSecrets()}, nil
}

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	provider := strArg(args, "provider")
	status := strArg(args, "status")
	rows, err := dbListInstances(ctx.AppDB(), provider, status)
	if err != nil {
		return nil, err
	}
	stripped := make([]*Instance, 0, len(rows))
	for _, r := range rows {
		stripped = append(stripped, r.stripSecrets())
	}
	return map[string]any{"instances": stripped, "count": len(stripped)}, nil
}

func (a *App) toolDestroy(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, ErrLocalInstanceImmutable
	}
	inst, err := dbGetInstance(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if err := destroyManagedInstance(ctx, inst); err != nil {
		return nil, err
	}
	return map[string]any{"destroyed": true, "id": id}, nil
}

func (a *App) toolUpgrade(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	size := strArg(args, "size")
	inst, err := dbGetInstance(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	res, err := upgradeProviderInstance(ctx, inst, UpgradeInstanceInput{
		Size:        size,
		UpgradeDisk: boolArg(args, "upgrade_disk", false),
		Wait:        true,
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (a *App) toolRunCommand(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	cmd := strArg(args, "cmd")
	if cmd == "" {
		return nil, errors.New("cmd required")
	}
	timeout := time.Duration(intArg(args, "timeout_s", 30)) * time.Second
	inst, err := dbGetInstance(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	var output string
	var exit int
	if inst.IsLocal() {
		output, exit, err = runLocal(cmd, timeout)
	} else {
		if inst.Status != "ready" {
			return nil, fmt.Errorf("instance not ready (status=%s)", inst.Status)
		}
		output, exit, err = runSSH(inst, cmd, timeout)
	}
	res := map[string]any{
		"id":        id,
		"output":    output,
		"exit_code": exit,
	}
	if err != nil {
		res["error"] = err.Error()
	}
	return res, nil
}

func (a *App) toolUploadFile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	path := strArg(args, "path")
	contentB64 := strArg(args, "content_b64")
	if path == "" || contentB64 == "" {
		return nil, errors.New("path and content_b64 required")
	}
	inst, err := dbGetInstance(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	var n int
	if inst.IsLocal() {
		n, err = uploadLocal(ctx, path, contentB64)
	} else {
		if inst.Status != "ready" {
			return nil, fmt.Errorf("instance not ready (status=%s)", inst.Status)
		}
		n, err = uploadSSH(inst, path, contentB64)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "path": path, "bytes_written": n}, nil
}

func (a *App) toolDownloadFile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	path := strArg(args, "path")
	if path == "" {
		return nil, errors.New("path required")
	}
	inst, err := dbGetInstance(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	var contentB64 string
	var n int
	if inst.IsLocal() {
		contentB64, n, err = downloadLocal(ctx, path)
	} else {
		if inst.Status != "ready" {
			return nil, fmt.Errorf("instance not ready (status=%s)", inst.Status)
		}
		contentB64, n, err = downloadSSH(inst, path)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "path": path, "content_b64": contentB64, "bytes": n}, nil
}

func (a *App) toolWaitReady(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	timeout := time.Duration(intArg(args, "timeout_s", 300)) * time.Second
	inst, err := dbGetInstance(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if inst.IsLocal() || inst.Status == "ready" {
		return map[string]any{"ready": true, "id": id, "status": inst.Status}, nil
	}
	if inst.Status != "provisioning" {
		return nil, fmt.Errorf("instance cannot be marked ready from status=%s", inst.Status)
	}
	if err := probeSSHReadyFn(inst, timeout); err != nil {
		return nil, err
	}
	_, _ = updateInstanceAndEmit(ctx, id, map[string]any{"status": "ready", "ready_at": nowUTC()})
	return map[string]any{"ready": true, "id": id, "status": "ready"}, nil
}

func (a *App) toolMetrics(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	inst, err := dbGetInstance(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	m, err := collectMetrics(inst)
	if err != nil {
		return nil, err
	}
	return map[string]any{"instance_id": id, "metrics": m}, nil
}

func (a *App) toolListServerTypes(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	provider, err := resolveInstanceProvider(ctx, strArg(args, "provider"))
	if err != nil {
		return nil, err
	}
	types, err := listServerTypes(ctx, provider)
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": provider, "server_types": types, "count": len(types)}, nil
}

func (a *App) toolListLocations(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	provider, err := resolveInstanceProvider(ctx, strArg(args, "provider"))
	if err != nil {
		return nil, err
	}
	locs, err := listLocations(ctx, provider)
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": provider, "locations": locs, "count": len(locs)}, nil
}

func (a *App) toolListImages(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	provider, err := resolveInstanceProvider(ctx, strArg(args, "provider"))
	if err != nil {
		return nil, err
	}
	imgs, err := listImages(ctx, provider)
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": provider, "images": imgs, "count": len(imgs)}, nil
}

// ─── arg helpers ──────────────────────────────────────────────────

func strArg(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		}
	}
	return def
}

func int64Arg(args map[string]any, key string) int64 {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int64(n)
		case int:
			return int64(n)
		case int64:
			return n
		}
	}
	return 0
}

func boolArg(args map[string]any, key string, def bool) bool {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
