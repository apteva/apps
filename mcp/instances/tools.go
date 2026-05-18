package main

import (
	"errors"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "instance_create",
			Description: "Provision a new instance via the bound VPS provider. v0.1 supports provider=hetzner. " +
				"Args: name (req), provider? (default 'hetzner' if not 'local'), region?, size?, image?, tags_json?. " +
				"Local instance (id 0) is auto-seeded; passing provider=local is refused.",
			InputSchema: schemaObject(map[string]any{
				"name":      map[string]any{"type": "string"},
				"provider":  map[string]any{"type": "string"},
				"region":    map[string]any{"type": "string"},
				"size":      map[string]any{"type": "string"},
				"image":     map[string]any{"type": "string"},
				"tags_json": map[string]any{"type": "string"},
			}, []string{"name"}),
			Handler: a.toolCreate,
		},
		{
			Name:        "instance_get",
			Description: "Fetch one instance by id (0 for local).",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolGet,
		},
		{
			Name:        "instance_list",
			Description: "List instances. Optional filters: provider ('local'|'hetzner'), status.",
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
			Name: "instance_wait_ready",
			Description: "Poll the instance until SSH is reachable. Already 'ready' instances return immediately. " +
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
				"Returns name + cores + memory_gb + disk_gb + monthly/hourly price + deprecation flag + available_in (locations). " +
				"Use this to discover valid `size` values for instance_create instead of hardcoding — Hetzner deprecates types over time. " +
				"Args: provider? (default 'hetzner').",
			InputSchema: schemaObject(map[string]any{"provider": map[string]any{"type": "string"}}, nil),
			Handler:     a.toolListServerTypes,
		},
		{
			Name: "instance_list_locations",
			Description: "List the VPS regions available from the bound provider, live from upstream. " +
				"Returns name + city + country + network_zone for each. " +
				"Use to discover valid `region` values for instance_create. Args: provider? (default 'hetzner').",
			InputSchema: schemaObject(map[string]any{"provider": map[string]any{"type": "string"}}, nil),
			Handler:     a.toolListLocations,
		},
		{
			Name: "instance_list_images",
			Description: "List OS images available from the bound provider, live from upstream (system images only — snapshots/backups/apps excluded). " +
				"Returns name + os_flavor + os_version + architecture. " +
				"Use to discover valid `image` values for instance_create. Args: provider? (default 'hetzner').",
			InputSchema: schemaObject(map[string]any{"provider": map[string]any{"type": "string"}}, nil),
			Handler:     a.toolListImages,
		},
	}
}

// ─── Handlers ─────────────────────────────────────────────────────

func (a *App) toolCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name := strArg(args, "name")
	if name == "" {
		return nil, errors.New("name required")
	}
	provider := strArg(args, "provider")
	if provider == "" {
		provider = "hetzner"
	}
	if provider == "local" {
		return nil, ErrLocalInstanceImmutable
	}
	in := CreateInstanceInput{
		Name:     name,
		Provider: provider,
		Region:   strArg(args, "region"),
		Size:     strArg(args, "size"),
		Image:    strArg(args, "image"),
		TagsJSON: strArg(args, "tags_json"),
	}
	switch provider {
	case "hetzner":
		inst, err := hetznerProvision(ctx, in)
		if err != nil {
			return nil, err
		}
		return map[string]any{"instance": inst.stripSecrets()}, nil
	default:
		return nil, fmt.Errorf("provider %q not supported in v0.1 (only 'hetzner')", provider)
	}
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
	switch inst.Provider {
	case "hetzner":
		if err := hetznerDestroy(ctx, inst); err != nil {
			return nil, err
		}
	}
	if err := dbDeleteInstance(ctx.AppDB(), id); err != nil {
		return nil, err
	}
	return map[string]any{"destroyed": true, "id": id}, nil
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
	if err := probeSSHReady(inst, timeout); err != nil {
		return nil, err
	}
	_ = dbUpdateInstance(ctx.AppDB(), id, map[string]any{"status": "ready", "ready_at": nowUTC()})
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
	types, err := listServerTypes(ctx, strArg(args, "provider"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"server_types": types, "count": len(types)}, nil
}

func (a *App) toolListLocations(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	locs, err := listLocations(ctx, strArg(args, "provider"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"locations": locs, "count": len(locs)}, nil
}

func (a *App) toolListImages(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	imgs, err := listImages(ctx, strArg(args, "provider"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"images": imgs, "count": len(imgs)}, nil
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
