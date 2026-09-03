// Containers app: generic local Docker workload runtime for Apteva.
package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML string

type App struct {
	backend DockerBackend
}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("containers requires a db block")
	}
	globalCtx = ctx
	if a.backend == nil {
		a.backend = LocalDocker{}
	}
	if err := ensureLocalHost(ctx.AppDB()); err != nil {
		return err
	}
	if err := seedBlueprints(ctx.AppDB()); err != nil {
		return err
	}
	if err := scrubStoredSecrets(ctx.AppDB()); err != nil {
		return err
	}
	if n, err := markInterruptedWorkloads(ctx.AppDB()); err != nil {
		return err
	} else if n > 0 {
		ctx.Logger().Warn("marked interrupted creating workloads", "count", n)
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := a.backend.Probe(probeCtx)
	cancel()
	if err != nil {
		_ = updateHostProbe(ctx.AppDB(), false, err.Error())
		ctx.Logger().Warn("docker not available", "err", err.Error())
	} else {
		_ = updateHostProbe(ctx.AppDB(), true, "")
	}
	if err := reconcileExecutions(context.Background(), ctx, a); err != nil {
		ctx.Logger().Warn("execution reconciliation failed", "err", err)
	}
	ctx.Logger().Info("containers mounted", "data_dir", ctx.DataDir())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name:     "health-poll",
			Schedule: "@every 30s",
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				return a.pollHealth(ctx, app, app.AppDB())
			},
		},
		{
			Name:     "execution-retention",
			Schedule: "@every 1h",
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				return retainExecutionLogs(ctx, app, a)
			},
		},
	}
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/api/workloads", Handler: a.handleWorkloads},
		{Pattern: "/api/workloads/", Handler: a.handleWorkloadItem},
		{Pattern: "/api/hosts", Handler: a.handleHosts},
		{Pattern: "/api/blueprints", Handler: a.handleBlueprints},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "containers_run", Description: "Run a Docker image as a managed workload.", InputSchema: runSchema(), HandlerCtx: a.toolRunCtx},
		{Name: "containers_create", Description: "Alias of containers_run.", InputSchema: runSchema(), HandlerCtx: a.toolRunCtx},
		{Name: "containers_get", Description: "Fetch one workload.", InputSchema: idSchema(), HandlerCtx: a.toolGetCtx},
		{Name: "containers_list", Description: "List workloads visible to the caller.", InputSchema: schemaObject(map[string]any{"status": map[string]any{"type": "string"}}, nil), HandlerCtx: a.toolListCtx},
		{Name: "containers_start", Description: "Start a stopped workload.", InputSchema: idSchema(), HandlerCtx: a.toolStartCtx},
		{Name: "containers_stop", Description: "Stop a running workload.", InputSchema: idSchema(), HandlerCtx: a.toolStopCtx},
		{Name: "containers_restart", Description: "Restart a workload.", InputSchema: idSchema(), HandlerCtx: a.toolRestartCtx},
		{Name: "containers_destroy", Description: "Destroy a workload.", InputSchema: workloadIDSchema(map[string]any{"delete_volumes": map[string]any{"type": "boolean"}}), HandlerCtx: a.toolDestroyCtx},
		{Name: "containers_logs", Description: "Tail workload logs.", InputSchema: workloadIDSchema(map[string]any{"tail": map[string]any{"type": "integer"}}), HandlerCtx: a.toolLogsCtx},
		{Name: "containers_health", Description: "Probe workload health.", InputSchema: idSchema(), HandlerCtx: a.toolHealthCtx},
		{Name: "containers_usage_get", Description: "Measure generic workload usage metrics such as container volume storage bytes.", InputSchema: idSchema(), HandlerCtx: a.toolUsageGetCtx},
		{Name: "containers_blueprints_list", Description: "List blueprints.", InputSchema: schemaObject(nil, nil), Handler: a.toolBlueprints},
		{Name: "containers_exec_start", Description: "Start an asynchronous command in an isolated execution container that shares an owned workload's image, network, and named volumes.", InputSchema: executionStartSchema(), Exposure: sdk.ToolExposureAppOnly, HandlerCtx: a.toolExecutionStart},
		{Name: "containers_exec_get", Description: "Fetch one owned execution.", InputSchema: executionIDSchema(), Exposure: sdk.ToolExposureAppOnly, HandlerCtx: a.toolExecutionGet},
		{Name: "containers_exec_logs", Description: "Tail bounded logs for one owned execution.", InputSchema: executionLogsSchema(), Exposure: sdk.ToolExposureAppOnly, HandlerCtx: a.toolExecutionLogs},
		{Name: "containers_exec_cancel", Description: "Cancel one owned queued or running execution.", InputSchema: executionIDSchema(), Exposure: sdk.ToolExposureAppOnly, HandlerCtx: a.toolExecutionCancel},
		{Name: "containers_volume_import", Description: "Import a bounded tar.gz archive into an attached named volume owned by the caller.", InputSchema: volumeImportSchema(), Exposure: sdk.ToolExposureAppOnly, HandlerCtx: a.toolVolumeImport},
		{Name: "containers_volume_export", Description: "Export a bounded tar.gz archive from an attached named volume owned by the caller.", InputSchema: volumeExportSchema(), Exposure: sdk.ToolExposureAppOnly, HandlerCtx: a.toolVolumeExport},
	}
}

func main() { sdk.Run(&App{}) }

func (a *App) createWorkload(ctx context.Context, appCtx *sdk.AppCtx, db *sql.DB, in RunSpec) (*Workload, error) {
	return a.createOwnedWorkload(ctx, appCtx, db, in, ownerIdentity{})
}

func (a *App) createOwnedWorkload(ctx context.Context, appCtx *sdk.AppCtx, db *sql.DB, in RunSpec, owner ownerIdentity) (*Workload, error) {
	w, spec, err := a.prepareOwnedWorkload(appCtx, db, in, owner)
	if err != nil {
		return nil, err
	}
	log.Printf("[containers] mcp create start workload_id=%s name=%q image=%q ports=%s volumes=%s",
		w.ID, spec.Name, spec.Image, describePorts(spec.Ports), describeVolumes(spec.Volumes))
	if err := a.startWorkloadRuntime(ctx, appCtx, db, w.ID, spec, w.ContainerName, w.NetworkName); err != nil {
		log.Printf("[containers] mcp create failed workload_id=%s err=%q", w.ID, err.Error())
		return nil, err
	}
	log.Printf("[containers] mcp create done workload_id=%s", w.ID)
	return getWorkload(db, w.ID)
}

func (a *App) queueWorkload(appCtx *sdk.AppCtx, db *sql.DB, in RunSpec) (*Workload, error) {
	w, spec, err := a.prepareOwnedWorkload(appCtx, db, in, ownerIdentity{})
	if err != nil {
		return nil, err
	}
	log.Printf("[containers] queued workload_id=%s name=%q image=%q container=%q network=%q ports=%s volumes=%s env_keys=%s",
		w.ID, spec.Name, spec.Image, w.ContainerName, w.NetworkName, describePorts(spec.Ports), describeVolumes(spec.Volumes), describeEnvKeys(spec.Env))
	go func(id, containerName, networkName string, spec RunSpec) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := a.startWorkloadRuntime(ctx, appCtx, db, id, spec, containerName, networkName); err != nil {
			log.Printf("[containers] async start failed workload_id=%s err=%q", id, err.Error())
		} else {
			log.Printf("[containers] async start done workload_id=%s", id)
		}
	}(w.ID, w.ContainerName, w.NetworkName, spec)
	return getWorkload(db, w.ID)
}

func (a *App) prepareWorkload(appCtx *sdk.AppCtx, db *sql.DB, in RunSpec) (*Workload, RunSpec, error) {
	return a.prepareOwnedWorkload(appCtx, db, in, ownerIdentity{})
}

func (a *App) prepareOwnedWorkload(appCtx *sdk.AppCtx, db *sql.DB, in RunSpec, owner ownerIdentity) (*Workload, RunSpec, error) {
	log.Printf("[containers] prepare begin name=%q image=%q blueprint=%q ports=%s volumes=%s env_keys=%s",
		in.Name, in.Image, in.BlueprintSlug, describePorts(in.Ports), describeVolumes(in.Volumes), describeEnvKeys(in.Env))
	spec, err := a.expandBlueprint(db, in)
	if err != nil {
		log.Printf("[containers] prepare expand failed name=%q err=%q", in.Name, err.Error())
		return nil, spec, err
	}
	if runSpecTargetID(spec) == 0 && !spec.UseLocal {
		if defaultID := defaultHostID(appCtx); defaultID > 0 {
			spec.HostID = defaultID
			spec.InstanceID = defaultID
		}
	}
	spec, err = normalizeRunSpec(spec)
	if err != nil {
		log.Printf("[containers] prepare normalize failed name=%q image=%q err=%q", in.Name, in.Image, err.Error())
		return nil, spec, err
	}
	for i := range spec.Ports {
		if spec.Ports[i].HostPort == 0 {
			p, err := freePort()
			if err != nil {
				log.Printf("[containers] prepare free_port failed name=%q err=%q", spec.Name, err.Error())
				return nil, spec, err
			}
			spec.Ports[i].HostPort = p
			log.Printf("[containers] allocated host port workload_name=%q container_port=%d host_port=%d",
				spec.Name, spec.Ports[i].ContainerPort, p)
		}
	}
	id := newWorkloadID()
	spec.runtimeWorkloadID = id
	runtimeSuffix := strings.TrimPrefix(id, "wrk_")
	if len(runtimeSuffix) > 8 {
		runtimeSuffix = runtimeSuffix[:8]
	}
	containerName := "containers-" + dockerSafeName(spec.Name) + "-" + runtimeSuffix
	networkName := containerName
	for i := range spec.Volumes {
		spec.Volumes[i].DockerVolumeName = containerName + "-" + spec.Volumes[i].Name
	}
	targetID := runSpecTargetID(spec)
	w := &Workload{
		ID: id, Name: spec.Name, BlueprintSlug: spec.BlueprintSlug, HostID: targetID, InstanceID: targetID,
		Kind: "container", Image: spec.Image, Status: StatusCreating, DesiredStatus: StatusRunning,
		ContainerName: containerName, NetworkName: networkName, HealthStatus: "unknown",
		HealthPath: spec.HealthPath, ConfigJSON: encodeJSON(sanitizeRunSpecForStorage(spec)), Env: redactEnvironment(spec.Env), EnvKeys: envKeys(spec.Env),
		EnvJSON: encodeJSON(redactEnvironment(spec.Env)), Resources: spec.Resources, ResourcesJSON: encodeJSON(spec.Resources),
		RestartPolicy: spec.RestartPolicy, Command: spec.Command,
		WorkingDirectory: spec.WorkingDirectory, User: spec.User,
		OwnerAppInstallID: owner.InstallID, OwnerAppName: owner.AppName, ProjectID: owner.ProjectID,
	}
	if len(spec.Ports) > 0 {
		p := spec.Ports[0]
		host := p.BindAddr
		if targetID != 0 {
			if remoteHost := remotePublicHost(appCtx, targetID); remoteHost != "" {
				host = remoteHost
			}
		}
		w.HealthURL = fmt.Sprintf("http://%s:%d%s", host, p.HostPort, spec.HealthPath)
		w.PublicURL = fmt.Sprintf("http://%s:%d", host, p.HostPort)
	}
	if err := insertWorkload(db, w, spec.Ports, spec.Volumes); err != nil {
		log.Printf("[containers] insert workload failed name=%q image=%q err=%q", spec.Name, spec.Image, err.Error())
		return nil, spec, err
	}
	log.Printf("[containers] prepared workload_id=%s name=%q image=%q status=%s health_url=%q public_url=%q",
		w.ID, w.Name, w.Image, w.Status, w.HealthURL, w.PublicURL)
	return w, spec, nil
}

func (a *App) startWorkloadRuntime(ctx context.Context, appCtx *sdk.AppCtx, db *sql.DB, id string, spec RunSpec, containerName, networkName string) error {
	spec.runtimeWorkloadID = id
	start := time.Now()
	log.Printf("[containers] runtime start workload_id=%s name=%q image=%q container=%q network=%q ports=%s volumes=%s",
		id, spec.Name, spec.Image, containerName, networkName, describePorts(spec.Ports), describeVolumes(spec.Volumes))
	fail := func(err error) error {
		log.Printf("[containers] runtime error workload_id=%s duration=%s err=%q",
			id, time.Since(start).Round(time.Millisecond), err.Error())
		_ = updateWorkload(db, id, map[string]any{"status": StatusError, "last_error": err.Error(), "updated_at": nowUTC()})
		_ = recordEvent(db, id, "error", "runtime", map[string]any{"error": err.Error()})
		return err
	}
	backend, err := a.backendForTarget(appCtx, runSpecTargetID(spec))
	if err != nil {
		return fail(err)
	}
	createdNetwork := false
	createdVolumes := make([]string, 0, len(spec.Volumes))
	runAttempted := false
	cleanupRuntime := func(cause error) error {
		if err := cleanupFailedRuntime(db, id, backend, containerName, runAttempted, networkName, createdNetwork, createdVolumes); err != nil {
			log.Printf("[containers] runtime cleanup failed workload_id=%s err=%q", id, err.Error())
		}
		return fail(cause)
	}
	log.Printf("[containers] runtime create_network workload_id=%s network=%q", id, networkName)
	createdNetwork, err = backend.EnsureNetwork(ctx, networkName)
	if err != nil {
		return fail(err)
	}
	for _, v := range spec.Volumes {
		log.Printf("[containers] runtime create_volume workload_id=%s volume=%q mount=%q", id, v.DockerVolumeName, v.MountPath)
		created, err := backend.EnsureVolume(ctx, v.DockerVolumeName)
		if err != nil {
			return cleanupRuntime(err)
		}
		if created {
			createdVolumes = append(createdVolumes, v.DockerVolumeName)
		}
	}
	writes, err := resolveFileWrites(spec)
	if err != nil {
		return cleanupRuntime(err)
	}
	for _, write := range writes {
		log.Printf("[containers] runtime write_file workload_id=%s path=%q volume=%q mode=%s secret=%t bytes=%d",
			id, write.Path, write.VolumeName, write.Mode, write.Secret, len(write.Content))
		if err := backend.WriteVolumeFile(ctx, write.VolumeName, write.RelPath, write.Content, write.Mode); err != nil {
			return cleanupRuntime(err)
		}
	}
	log.Printf("[containers] runtime docker_run workload_id=%s container=%q image=%q", id, containerName, spec.Image)
	runAttempted = true
	cid, err := backend.Run(ctx, spec, containerName, networkName)
	if err != nil {
		return cleanupRuntime(err)
	}
	log.Printf("[containers] runtime docker_run ok workload_id=%s container_id=%s", id, cid)
	if err := updateWorkload(db, id, map[string]any{"status": StatusRunning, "container_id": cid, "last_error": "", "updated_at": nowUTC()}); err != nil {
		return cleanupRuntime(fmt.Errorf("record running workload: %w", err))
	}
	_ = recordEvent(db, id, "started", "tool", map[string]any{"container_id": cid})
	log.Printf("[containers] runtime probe workload_id=%s", id)
	_ = a.probeWorkload(ctx, appCtx, db, id)
	log.Printf("[containers] runtime done workload_id=%s duration=%s", id, time.Since(start).Round(time.Millisecond))
	return nil
}

func cleanupFailedRuntime(db *sql.DB, id string, backend DockerBackend, containerName string, removeContainer bool, networkName string, removeNetwork bool, volumes []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var errs []error
	if removeContainer && strings.TrimSpace(containerName) != "" {
		if err := backend.RemoveManagedContainer(ctx, containerName, id); err != nil && !isDockerMissingResourceError(err, "container") {
			errs = append(errs, fmt.Errorf("remove container %s: %w", containerName, err))
		}
	}
	for _, volumeName := range volumes {
		if err := backend.RemoveVolume(ctx, volumeName); err != nil && !isDockerMissingResourceError(err, "volume") {
			errs = append(errs, fmt.Errorf("remove volume %s: %w", volumeName, err))
		}
	}
	if removeNetwork && strings.TrimSpace(networkName) != "" {
		if err := backend.RemoveNetwork(ctx, networkName); err != nil && !isDockerMissingResourceError(err, "network") {
			errs = append(errs, fmt.Errorf("remove network %s: %w", networkName, err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	err := errors.Join(errs...)
	_ = recordEvent(db, id, "cleanup_error", "runtime", map[string]any{"error": err.Error()})
	return err
}

func (a *App) expandBlueprint(db *sql.DB, in RunSpec) (RunSpec, error) {
	if strings.TrimSpace(in.BlueprintSlug) == "" {
		return in, nil
	}
	bp, err := getBlueprint(db, in.BlueprintSlug)
	if err != nil {
		return in, err
	}
	if bp == nil {
		return in, fmt.Errorf("blueprint %q not found", in.BlueprintSlug)
	}
	base := bp.Spec
	base.BlueprintSlug = bp.Slug
	base.UseLocal = in.UseLocal
	if targetID := runSpecTargetID(in); targetID != 0 {
		base.HostID = targetID
		base.InstanceID = targetID
	} else if in.UseLocal {
		base.HostID = 0
		base.InstanceID = 0
	}
	if in.Name != "" {
		base.Name = in.Name
	}
	if in.Image != "" {
		base.Image = in.Image
	}
	if len(in.Ports) > 0 {
		base.Ports = in.Ports
	}
	if in.Env != nil {
		if base.Env == nil {
			base.Env = map[string]string{}
		}
		for k, v := range in.Env {
			base.Env[k] = v
		}
	}
	if len(in.Volumes) > 0 {
		base.Volumes = in.Volumes
	}
	if len(in.Files) > 0 {
		base.Files = in.Files
	}
	if in.PullPolicy != "" {
		base.PullPolicy = in.PullPolicy
	}
	if in.HealthPath != "" {
		base.HealthPath = in.HealthPath
	}
	if in.Resources.MemoryMB != 0 || in.Resources.CPU != 0 {
		base.Resources = in.Resources
	}
	if in.RestartPolicy != "" {
		base.RestartPolicy = in.RestartPolicy
	}
	if in.Command != nil {
		base.Command = in.Command
	}
	if in.WorkingDirectory != "" {
		base.WorkingDirectory = in.WorkingDirectory
	}
	if in.User != "" {
		base.User = in.User
	}
	return base, nil
}

func (a *App) startWorkload(ctx context.Context, appCtx *sdk.AppCtx, db *sql.DB, id string) (*Workload, error) {
	w, err := requireWorkload(db, id)
	if err != nil {
		return nil, err
	}
	backend, err := a.backendForWorkload(appCtx, w)
	if err != nil {
		return nil, err
	}
	if err := backend.Start(ctx, w.ContainerName); err != nil {
		_ = updateWorkload(db, id, map[string]any{"status": StatusError, "last_error": err.Error(), "updated_at": nowUTC()})
		return nil, err
	}
	_ = updateWorkload(db, id, map[string]any{"status": StatusRunning, "desired_status": StatusRunning, "last_error": "", "updated_at": nowUTC()})
	_ = recordEvent(db, id, "started", "tool", map[string]any{})
	_ = a.probeWorkload(ctx, appCtx, db, id)
	return getWorkload(db, id)
}

func (a *App) stopWorkload(ctx context.Context, appCtx *sdk.AppCtx, db *sql.DB, id string) (*Workload, error) {
	w, err := requireWorkload(db, id)
	if err != nil {
		return nil, err
	}
	backend, err := a.backendForWorkload(appCtx, w)
	if err != nil {
		return nil, err
	}
	if err := backend.Stop(ctx, w.ContainerName); err != nil {
		_ = updateWorkload(db, id, map[string]any{"status": StatusError, "health_status": "error", "last_error": err.Error(), "updated_at": nowUTC()})
		return nil, err
	}
	_ = updateWorkload(db, id, map[string]any{"status": StatusStopped, "desired_status": StatusStopped, "health_status": "stopped", "last_error": "", "updated_at": nowUTC()})
	_ = recordEvent(db, id, "stopped", "tool", map[string]any{})
	return getWorkload(db, id)
}

func (a *App) restartWorkload(ctx context.Context, appCtx *sdk.AppCtx, db *sql.DB, id string) (*Workload, error) {
	w, err := requireWorkload(db, id)
	if err != nil {
		return nil, err
	}
	backend, err := a.backendForWorkload(appCtx, w)
	if err != nil {
		return nil, err
	}
	if err := backend.Restart(ctx, w.ContainerName); err != nil {
		_ = updateWorkload(db, id, map[string]any{"status": StatusError, "health_status": "error", "last_error": err.Error(), "updated_at": nowUTC()})
		return nil, err
	}
	_ = updateWorkload(db, id, map[string]any{"status": StatusRunning, "desired_status": StatusRunning, "last_error": "", "updated_at": nowUTC()})
	_ = recordEvent(db, id, "restarted", "tool", map[string]any{})
	_ = a.probeWorkload(ctx, appCtx, db, id)
	return getWorkload(db, id)
}

func (a *App) destroyWorkload(ctx context.Context, appCtx *sdk.AppCtx, db *sql.DB, id string, deleteVolumes bool) error {
	w, err := requireWorkload(db, id)
	if err != nil {
		return err
	}
	if err := a.cancelWorkloadExecutions(appCtx, w.ID); err != nil {
		return fmt.Errorf("cancel active executions: %w", err)
	}
	backend, err := a.backendForWorkload(appCtx, w)
	if err != nil {
		return err
	}
	var cleanupErrs []error
	if err := backend.Remove(ctx, w.ContainerName, true); err != nil && !isDockerMissingResourceError(err, "container") {
		cleanupErrs = append(cleanupErrs, err)
	}
	cleanupErrs = append(cleanupErrs, removeWorkloadExecutionContainers(ctx, db, backend, w.ID)...)
	if err := backend.RemoveNetwork(ctx, w.NetworkName); err != nil && !isDockerMissingResourceError(err, "network") {
		cleanupErrs = append(cleanupErrs, err)
	}
	if deleteVolumes {
		for _, v := range w.Volumes {
			if err := backend.RemoveVolume(ctx, v.DockerVolumeName); err != nil && !isDockerMissingResourceError(err, "volume") {
				cleanupErrs = append(cleanupErrs, err)
			}
		}
	}
	if len(cleanupErrs) > 0 {
		err := errors.Join(cleanupErrs...)
		_ = updateWorkload(db, id, map[string]any{"status": StatusError, "health_status": "error", "last_error": err.Error(), "updated_at": nowUTC()})
		_ = recordEvent(db, id, "destroy_failed", "tool", map[string]any{"error": err.Error()})
		return err
	}
	return deleteWorkloadRows(db, id)
}

func (a *App) probeWorkload(ctx context.Context, appCtx *sdk.AppCtx, db *sql.DB, id string) error {
	w, err := requireWorkload(db, id)
	if err != nil {
		return err
	}
	backend, err := a.backendForWorkload(appCtx, w)
	if err != nil {
		_ = updateWorkload(db, id, map[string]any{"status": StatusError, "health_status": "error", "last_health_at": nowUTC(), "last_error": err.Error(), "updated_at": nowUTC()})
		return err
	}
	state, err := backend.Inspect(ctx, w.ContainerName)
	if err != nil {
		_ = updateWorkload(db, id, map[string]any{"status": StatusError, "health_status": "error", "last_health_at": nowUTC(), "last_error": err.Error(), "updated_at": nowUTC()})
		return err
	}
	status := StatusStopped
	if state.Running {
		status = StatusRunning
	}
	health := state.Health
	if health == "" {
		if state.Running && w.HealthURL != "" {
			hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err = probeHTTP(hctx, w.HealthURL)
			cancel()
			if err == nil {
				health = "healthy"
			} else {
				health = "unhealthy"
			}
		} else if state.Running {
			health = "running"
		} else {
			health = "stopped"
		}
	}
	fields := map[string]any{
		"status": status, "container_id": state.ID, "health_status": health,
		"last_health_at": nowUTC(), "updated_at": nowUTC(),
	}
	if health == "unhealthy" && err != nil {
		fields["last_error"] = err.Error()
	} else {
		fields["last_error"] = ""
	}
	return updateWorkload(db, id, fields)
}

func (a *App) pollHealth(ctx context.Context, appCtx *sdk.AppCtx, db *sql.DB) error {
	rows, err := listWorkloads(db, "")
	if err != nil {
		return err
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, w := range rows {
		if w.Status == StatusDestroyed {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			_ = a.probeWorkload(ctx, appCtx, db, id)
		}(w.ID)
	}
	wg.Wait()
	return ctx.Err()
}

func (a *App) workloadUsage(ctx context.Context, appCtx *sdk.AppCtx, db *sql.DB, id string) (*WorkloadUsage, error) {
	w, err := requireWorkload(db, id)
	if err != nil {
		return nil, err
	}
	backend, err := a.backendForWorkload(appCtx, w)
	if err != nil {
		return nil, err
	}
	usage := &WorkloadUsage{WorkloadID: w.ID, UpdatedAt: nowUTC()}
	var total int64
	for _, volume := range w.Volumes {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		size, err := backend.VolumeUsage(ctx, volume.DockerVolumeName)
		if err != nil {
			return nil, err
		}
		total += size
		_ = updateVolumeSize(db, w.ID, volume.Name, size)
		usage.Metrics = append(usage.Metrics, UsageMetric{
			FeatureKey: "containers.storage.bytes",
			Quantity:   size,
			Unit:       "bytes",
			Kind:       "gauge",
			Source:     "docker_volume",
			Dimensions: map[string]string{
				"volume":             volume.Name,
				"docker_volume_name": volume.DockerVolumeName,
				"mount_path":         volume.MountPath,
			},
		})
	}
	usage.Metrics = append([]UsageMetric{{
		FeatureKey: "containers.storage.bytes",
		Quantity:   total,
		Unit:       "bytes",
		Kind:       "gauge",
		Source:     "docker_volume_total",
		Dimensions: map[string]string{"workload_id": w.ID},
	}}, usage.Metrics...)
	return usage, nil
}

func (a *App) backendForWorkload(appCtx *sdk.AppCtx, w *Workload) (DockerBackend, error) {
	return a.backendForTarget(appCtx, workloadTargetID(w))
}

func (a *App) backendForTarget(appCtx *sdk.AppCtx, targetID int64) (DockerBackend, error) {
	if targetID == 0 {
		if a.backend == nil {
			a.backend = LocalDocker{}
		}
		return a.backend, nil
	}
	if appCtx == nil || appCtx.PlatformAPI() == nil {
		return nil, errors.New("remote container target requires platform app calls")
	}
	return &RemoteDocker{app: appCtx, instanceID: targetID}, nil
}

func runSpecTargetID(spec RunSpec) int64 {
	if spec.InstanceID != 0 {
		return spec.InstanceID
	}
	return spec.HostID
}

func workloadTargetID(w *Workload) int64 {
	if w.InstanceID != 0 {
		return w.InstanceID
	}
	return w.HostID
}

func remotePublicHost(appCtx *sdk.AppCtx, instanceID int64) string {
	if appCtx == nil || appCtx.PlatformAPI() == nil || instanceID <= 0 {
		return ""
	}
	var resp struct {
		Instance struct {
			PublicIPv4 string `json:"public_ipv4"`
			PublicIPv6 string `json:"public_ipv6"`
		} `json:"instance"`
	}
	err := appCtx.PlatformAPI().CallAppResult("instances", "instance_get", map[string]any{"id": instanceID}, &resp)
	if err != nil {
		log.Printf("[containers] remote public host lookup failed instance_id=%d err=%q", instanceID, err.Error())
		return ""
	}
	if strings.TrimSpace(resp.Instance.PublicIPv4) != "" {
		return strings.TrimSpace(resp.Instance.PublicIPv4)
	}
	if strings.TrimSpace(resp.Instance.PublicIPv6) != "" {
		return "[" + strings.Trim(resp.Instance.PublicIPv6, "[]") + "]"
	}
	return ""
}

func requireWorkload(db *sql.DB, id string) (*Workload, error) {
	w, err := getWorkload(db, id)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, fmt.Errorf("workload %q not found", id)
	}
	return w, nil
}

func isDockerMissingResourceError(err error, resource string) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch resource {
	case "container":
		return strings.Contains(msg, "no such container")
	case "network":
		return strings.Contains(msg, "no such network")
	case "volume":
		return strings.Contains(msg, "no such volume")
	default:
		return false
	}
}

func updateHostProbe(db *sql.DB, ok bool, errMsg string) error {
	now := nowUTC()
	status := "ready"
	available := 1
	if !ok {
		status = "error"
		available = 0
	}
	_, err := db.Exec(`UPDATE containers_hosts SET status=?, docker_available=?, last_probe_at=?, last_error=?, updated_at=? WHERE id=0`,
		status, available, now, errMsg, now)
	return err
}

func markInterruptedWorkloads(db *sql.DB) (int64, error) {
	res, err := db.Exec(`
		UPDATE containers_workloads
		SET status='error',
			desired_status='stopped',
			health_status='error',
			last_error='interrupted while starting; destroy and run again',
			updated_at=?
		WHERE status='creating'`, nowUTC())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Tools.

func (a *App) toolRun(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spec := parseRunSpec(args)
	cctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	w, err := a.createWorkload(cctx, ctx, ctx.AppDB(), spec)
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload": w}, nil
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	w, err := requireWorkload(ctx.AppDB(), workloadIDArg(args))
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload": w}, nil
}

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := listWorkloads(ctx.AppDB(), getStr(args, "status"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"workloads": rows, "count": len(rows)}, nil
}

func (a *App) toolStart(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w, err := a.startWorkload(cctx, ctx, ctx.AppDB(), workloadIDArg(args))
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload": w}, nil
}

func (a *App) toolStop(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w, err := a.stopWorkload(cctx, ctx, ctx.AppDB(), workloadIDArg(args))
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload": w}, nil
}

func (a *App) toolRestart(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	w, err := a.restartWorkload(cctx, ctx, ctx.AppDB(), workloadIDArg(args))
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload": w}, nil
}

func (a *App) toolDestroy(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	id := workloadIDArg(args)
	if err := a.destroyWorkload(cctx, ctx, ctx.AppDB(), id, boolArg(args, "delete_volumes")); err != nil {
		return nil, err
	}
	return map[string]any{"destroyed": true, "workload_id": id}, nil
}

func (a *App) toolLogs(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	w, err := requireWorkload(ctx.AppDB(), workloadIDArg(args))
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	backend, err := a.backendForWorkload(ctx, w)
	if err != nil {
		return nil, err
	}
	logs, err := backend.Logs(cctx, w.ContainerName, intArg(args, "tail", 200))
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload_id": w.ID, "logs": logs}, nil
}

func (a *App) toolHealth(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := workloadIDArg(args)
	if err := a.probeWorkload(cctx, ctx, ctx.AppDB(), id); err != nil {
		return nil, err
	}
	w, _ := getWorkload(ctx.AppDB(), id)
	return map[string]any{"workload": w}, nil
}

func (a *App) toolUsageGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	usage, err := a.workloadUsage(cctx, ctx, ctx.AppDB(), workloadIDArg(args))
	if err != nil {
		return nil, err
	}
	return map[string]any{"usage": usage, "metrics": usage.Metrics}, nil
}

func (a *App) toolBlueprints(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	bps, err := listBlueprints(ctx.AppDB())
	if err != nil {
		return nil, err
	}
	return map[string]any{"blueprints": bps}, nil
}

// HTTP.

func (a *App) handleWorkloads(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := listWorkloads(globalCtx.AppDB(), r.URL.Query().Get("status"))
		writeResult(w, map[string]any{"workloads": rows, "count": len(rows)}, err)
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, maxRunRequestBytes)
		var spec RunSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("[containers] http POST /api/workloads name=%q image=%q blueprint=%q ports=%s volumes=%s env_keys=%s",
			spec.Name, spec.Image, spec.BlueprintSlug, describePorts(spec.Ports), describeVolumes(spec.Volumes), describeEnvKeys(spec.Env))
		res, err := a.queueWorkload(globalCtx, globalCtx.AppDB(), spec)
		writeResult(w, map[string]any{"workload": res}, err)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleWorkloadItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/workloads/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "workload id required", http.StatusBadRequest)
		return
	}
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch {
	case r.Method == http.MethodGet && action == "":
		wk, err := requireWorkload(globalCtx.AppDB(), id)
		writeResult(w, map[string]any{"workload": wk}, err)
	case r.Method == http.MethodDelete && action == "":
		err := a.destroyWorkload(r.Context(), globalCtx, globalCtx.AppDB(), id, r.URL.Query().Get("delete_volumes") == "1")
		writeResult(w, map[string]any{"destroyed": err == nil, "workload_id": id}, err)
	case r.Method == http.MethodPost && action == "start":
		wk, err := a.startWorkload(r.Context(), globalCtx, globalCtx.AppDB(), id)
		writeResult(w, map[string]any{"workload": wk}, err)
	case r.Method == http.MethodPost && action == "stop":
		wk, err := a.stopWorkload(r.Context(), globalCtx, globalCtx.AppDB(), id)
		writeResult(w, map[string]any{"workload": wk}, err)
	case r.Method == http.MethodPost && action == "restart":
		wk, err := a.restartWorkload(r.Context(), globalCtx, globalCtx.AppDB(), id)
		writeResult(w, map[string]any{"workload": wk}, err)
	case r.Method == http.MethodPost && action == "health":
		err := a.probeWorkload(r.Context(), globalCtx, globalCtx.AppDB(), id)
		wk, _ := getWorkload(globalCtx.AppDB(), id)
		writeResult(w, map[string]any{"workload": wk}, err)
	case r.Method == http.MethodGet && action == "usage":
		usage, err := a.workloadUsage(r.Context(), globalCtx, globalCtx.AppDB(), id)
		if usage == nil {
			writeResult(w, nil, err)
			return
		}
		writeResult(w, map[string]any{"usage": usage, "metrics": usage.Metrics}, err)
	case r.Method == http.MethodGet && action == "logs":
		wk, err := requireWorkload(globalCtx.AppDB(), id)
		if err != nil {
			writeResult(w, nil, err)
			return
		}
		backend, err := a.backendForWorkload(globalCtx, wk)
		if err != nil {
			writeResult(w, nil, err)
			return
		}
		logs, err := backend.Logs(r.Context(), wk.ContainerName, queryInt(r, "tail", 200))
		writeResult(w, map[string]any{"workload_id": id, "logs": logs}, err)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (a *App) handleBlueprints(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	bps, err := listBlueprints(globalCtx.AppDB())
	writeResult(w, map[string]any{"blueprints": bps}, err)
}

func (a *App) handleHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	hosts, warning := containerHosts(globalCtx)
	out := map[string]any{
		"hosts":           hosts,
		"default_host_id": defaultHostID(globalCtx),
	}
	if warning != "" {
		out["warning"] = warning
	}
	writeResult(w, out, nil)
}

func writeResult(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Arg helpers.

func parseRunSpec(args map[string]any) RunSpec {
	var spec RunSpec
	raw, _ := json.Marshal(args)
	_ = json.Unmarshal(raw, &spec)
	return spec
}

func getStr(args map[string]any, k string) string {
	v, _ := args[k].(string)
	return strings.TrimSpace(v)
}

func boolArg(args map[string]any, k string) bool {
	v, _ := args[k].(bool)
	return v
}

func intArg(args map[string]any, k string, def int) int {
	switch v := args[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

type containerHost struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Provider   string `json:"provider"`
	Status     string `json:"status"`
	PublicIPv4 string `json:"public_ipv4,omitempty"`
	Label      string `json:"label"`
	Default    bool   `json:"default"`
}

func defaultHostID(ctx *sdk.AppCtx) int64 {
	if ctx == nil || ctx.Config() == nil {
		return 0
	}
	raw := strings.TrimSpace(ctx.Config().Get("default_host_id"))
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func containerHosts(ctx *sdk.AppCtx) ([]containerHost, string) {
	defaultID := defaultHostID(ctx)
	hosts := []containerHost{formatContainerHost(containerHost{
		ID:       0,
		Name:     "localhost",
		Provider: "local",
		Status:   "ready",
	}, defaultID)}
	if ctx == nil || ctx.PlatformAPI() == nil {
		return hosts, ""
	}
	var resp struct {
		Instances []containerHost `json:"instances"`
	}
	if err := ctx.PlatformAPI().CallAppResult("instances", "instance_list", map[string]any{}, &resp); err != nil {
		return hosts, err.Error()
	}
	seen := map[int64]bool{0: true}
	for _, h := range resp.Instances {
		if seen[h.ID] {
			continue
		}
		seen[h.ID] = true
		hosts = append(hosts, formatContainerHost(h, defaultID))
	}
	return hosts, ""
}

func formatContainerHost(h containerHost, defaultID int64) containerHost {
	if h.Name == "" {
		h.Name = fmt.Sprintf("instance %d", h.ID)
	}
	if h.Provider == "" {
		h.Provider = "instances"
	}
	if h.Status == "" {
		h.Status = "unknown"
	}
	if h.Label == "" {
		h.Label = h.Name
		if h.ID == 0 {
			h.Label = "Local Docker host"
		} else if h.PublicIPv4 != "" {
			h.Label = fmt.Sprintf("%s (%s)", h.Name, h.PublicIPv4)
		}
	}
	h.Default = h.ID == defaultID
	return h
}

func describePorts(ports []PortSpec) string {
	if len(ports) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, fmt.Sprintf("%s:%d->%d/%s", p.BindAddr, p.HostPort, p.ContainerPort, p.Protocol))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func describeVolumes(volumes []VolumeSpec) string {
	if len(volumes) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(volumes))
	for _, v := range volumes {
		parts = append(parts, fmt.Sprintf("%s:%s", v.Name, v.MountPath))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func describeEnvKeys(env map[string]string) string {
	if len(env) == 0 {
		return "[]"
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "[" + strings.Join(keys, ",") + "]"
}

func schemaObject(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func idSchema() map[string]any {
	return workloadIDSchema(nil)
}

func workloadIDSchema(extra map[string]any) map[string]any {
	props := map[string]any{
		"workload_id": map[string]any{"type": "string"},
		"id":          map[string]any{"type": "string"},
	}
	for k, v := range extra {
		props[k] = v
	}
	return schemaObject(props, nil)
}

func workloadIDArg(args map[string]any) string {
	if id := getStr(args, "workload_id"); id != "" {
		return id
	}
	return getStr(args, "id")
}

func runSchema() map[string]any {
	return schemaObject(map[string]any{
		"name":              map[string]any{"type": "string"},
		"image":             map[string]any{"type": "string"},
		"blueprint_slug":    map[string]any{"type": "string"},
		"host_id":           map[string]any{"type": "integer"},
		"instance_id":       map[string]any{"type": "integer"},
		"use_local":         map[string]any{"type": "boolean"},
		"ports":             map[string]any{"type": "array"},
		"env":               map[string]any{"type": "object"},
		"volumes":           map[string]any{"type": "array"},
		"files":             map[string]any{"type": "array", "items": schemaObject(map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}, "content_base64": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string"}, "secret": map[string]any{"type": "boolean"}}, []string{"path"})},
		"pull_policy":       map[string]any{"type": "string", "enum": []string{"missing", "always", "never"}},
		"health_path":       map[string]any{"type": "string"},
		"resources":         map[string]any{"type": "object"},
		"restart_policy":    map[string]any{"type": "string"},
		"command":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"working_directory": map[string]any{"type": "string"},
		"user":              map[string]any{"type": "string"},
	}, []string{"name"})
}

func executionIDSchema() map[string]any {
	return schemaObject(map[string]any{"execution_id": map[string]any{"type": "string"}}, []string{"execution_id"})
}

func executionStartSchema() map[string]any {
	return schemaObject(map[string]any{
		"workload_id":       map[string]any{"type": "string"},
		"argv":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"shell_command":     map[string]any{"type": "string"},
		"working_directory": map[string]any{"type": "string"},
		"env":               map[string]any{"type": "object"},
		"timeout_s":         map[string]any{"type": "integer", "minimum": 1, "maximum": 86400},
		"idempotency_key":   map[string]any{"type": "string"},
	}, []string{"workload_id"})
}

func executionLogsSchema() map[string]any {
	return schemaObject(map[string]any{
		"execution_id": map[string]any{"type": "string"},
		"tail":         map[string]any{"type": "integer", "minimum": 1, "maximum": 2000},
	}, []string{"execution_id"})
}

func volumeImportSchema() map[string]any {
	return schemaObject(map[string]any{
		"workload_id":    map[string]any{"type": "string"},
		"volume":         map[string]any{"type": "string"},
		"path":           map[string]any{"type": "string"},
		"archive_base64": map[string]any{"type": "string"},
	}, []string{"workload_id", "volume", "archive_base64"})
}

func volumeExportSchema() map[string]any {
	return schemaObject(map[string]any{
		"workload_id": map[string]any{"type": "string"},
		"volume":      map[string]any{"type": "string"},
		"path":        map[string]any{"type": "string"},
	}, []string{"workload_id", "volume"})
}
