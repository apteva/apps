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
	admissionMu     sync.Mutex
	maintenanceMu   sync.Mutex
	legacyHealth    time.Time
	legacyExecution time.Time
	backend         DockerBackend
	guardMu         sync.Mutex
	guards          map[string]*workloadGuard
	executionMu     sync.Mutex
	supervisors     map[string]bool
	launches        map[string]bool
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
	if _, err := ctx.AppDB().Exec(`INSERT OR REPLACE INTO containers_runtime_cleanup(workload_id,project_id,retry_until) SELECT id,project_id,? FROM containers_workloads WHERE status='creating'`, time.Now().Add(20*time.Minute).UTC().Format(time.RFC3339)); err != nil {
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
	if _, local := a.backend.(LocalDocker); local && err == nil {
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		recoveryErr := a.recoverAllArchivePauses(recoveryCtx, ctx)
		if recoveryErr == nil {
			recoveryErr = a.recoverShellSessions(recoveryCtx, ctx)
		}
		cancel()
		if recoveryErr != nil {
			return recoveryErr
		}
	}
	if err := reconcileExecutions(context.Background(), ctx, a); err != nil {
		ctx.Logger().Warn("execution reconciliation failed", "err", err)
	}
	ctx.Logger().Info("containers mounted", "data_dir", ctx.DataDir())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	persistentShells.CloseAll()
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name:     "health-poll",
			Schedule: "@every 30s",
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				if a.claimLegacyTick(app, true) {
					if err := a.pollHealth(ctx, app.WithProject(""), app.AppDB()); err != nil {
						return err
					}
				}
				return a.pollHealth(ctx, app, app.AppDB())
			},
		},
		{
			Name:     "execution-retention",
			Schedule: "@every 10s",
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				return a.maintenance(ctx, app)
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
		{Name: "containers_list", Description: "List workloads visible to the caller.", InputSchema: schemaObject(map[string]any{"status": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}, "offset": map[string]any{"type": "integer", "minimum": 0}}, nil), HandlerCtx: a.toolListCtx},
		{Name: "containers_start", Description: "Start a stopped workload.", InputSchema: idSchema(), HandlerCtx: a.toolStartCtx},
		{Name: "containers_stop", Description: "Stop a running workload.", InputSchema: idSchema(), HandlerCtx: a.toolStopCtx},
		{Name: "containers_restart", Description: "Restart a workload.", InputSchema: idSchema(), HandlerCtx: a.toolRestartCtx},
		{Name: "containers_destroy", Description: "Destroy a workload.", InputSchema: workloadIDSchema(map[string]any{"delete_volumes": map[string]any{"type": "boolean"}}), HandlerCtx: a.toolDestroyCtx},
		{Name: "containers_logs", Description: "Tail workload logs.", InputSchema: workloadIDSchema(map[string]any{"tail": map[string]any{"type": "integer"}}), HandlerCtx: a.toolLogsCtx},
		{Name: "containers_health", Description: "Probe workload health.", InputSchema: idSchema(), HandlerCtx: a.toolHealthCtx},
		{Name: "containers_usage_get", Description: "Measure generic workload usage metrics such as container volume storage bytes.", InputSchema: idSchema(), HandlerCtx: a.toolUsageGetCtx},
		{Name: "containers_blueprints_list", Description: "List blueprints.", InputSchema: schemaObject(nil, nil), Handler: a.toolBlueprints},
		{Name: "containers_sessions_list", Description: "List owned persistent shell sessions.", InputSchema: idSchema(), Exposure: sdk.ToolExposureAppOnly, HandlerCtx: a.toolSessions},
		{Name: "containers_session_close", Description: "Close an idle owned shell session.", InputSchema: workloadIDSchema(map[string]any{"session_key": map[string]any{"type": "string"}}), Exposure: sdk.ToolExposureAppOnly, HandlerCtx: a.toolSessionClose},
		{Name: "containers_exec_start", Description: "Start an asynchronous command inside an owned workload container. Set session_key to reuse a stateful PTY shell across commands.", InputSchema: executionStartSchema(), Exposure: sdk.ToolExposureAppOnly, HandlerCtx: a.toolExecutionStart},
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
	owner := ownerIdentity{}
	if appCtx != nil {
		owner.ProjectID = appCtx.CurrentProject()
	}
	w, spec, err := a.prepareOwnedWorkload(appCtx, db, in, owner)
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
	a.admissionMu.Lock()
	defer a.admissionMu.Unlock()
	var creating, active int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status='creating' THEN 1 ELSE 0 END),0) FROM containers_workloads WHERE status!='destroyed'`).Scan(&active, &creating); err != nil {
		return nil, in, err
	}
	if creating >= 16 || active >= 1000 {
		return nil, in, fmt.Errorf("%w: workload capacity reached (16 creating, 1000 active maximum)", errConflict)
	}
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
	id := newWorkloadID()
	spec.runtimeWorkloadID = id
	runtimeSuffix := strings.TrimPrefix(id, "wrk_")
	if len(runtimeSuffix) > 8 {
		runtimeSuffix = runtimeSuffix[:8]
	}
	containerName := "containers-" + dockerSafeName(spec.Name) + "-" + runtimeSuffix
	networkName := containerName
	for i := range spec.Volumes {
		if spec.Volumes[i].RetainedFrom != "" {
			name, err := retainedVolume(db, spec.Volumes[i].RetainedFrom, spec.Volumes[i].Name, owner, runSpecTargetID(spec))
			if err != nil {
				return nil, spec, err
			}
			spec.Volumes[i].DockerVolumeName = name
		} else {
			spec.Volumes[i].DockerVolumeName = containerName + "-" + spec.Volumes[i].Name
		}
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
	if len(spec.Ports) > 0 && spec.Ports[0].HostPort > 0 && spec.Ports[0].Protocol == "tcp" {
		p := spec.Ports[0]
		host := p.BindAddr
		if targetID != 0 {
			if remoteHost := remotePublicHost(appCtx, targetID); remoteHost != "" {
				host = remoteHost
			}
		}
		if spec.HealthPath != "" && !spec.DisableHealthCheck {
			w.HealthURL = fmt.Sprintf("%s://%s:%d%s", spec.HealthScheme, host, p.HostPort, spec.HealthPath)
		}
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
	ctx, unlock, err := a.lockWorkload(ctx, id, true)
	if err != nil {
		return err
	}
	defer unlock()
	existing, err := requireWorkload(db, id)
	if err != nil {
		return err
	}
	if existing.Status != StatusCreating {
		return errConflict
	}

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
		if runAttempted {
			if _, err := db.Exec(`INSERT OR REPLACE INTO containers_runtime_cleanup(workload_id,project_id,retry_until) SELECT id,project_id,? FROM containers_workloads WHERE id=?`, time.Now().Add(20*time.Minute).UTC().Format(time.RFC3339), id); err != nil {
				cause = errors.Join(cause, err)
			}
		}
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
	defaultOwner := ""
	if writer, ok := backend.(ownedFileWriter); ok {
		for _, write := range writes {
			if write.Owner == "" {
				defaultOwner, err = writer.FileOwner(ctx, spec)
				if err != nil {
					return cleanupRuntime(err)
				}
				break
			}
		}
	}
	for _, write := range writes {
		log.Printf("[containers] runtime write_file workload_id=%s path=%q volume=%q mode=%s secret=%t bytes=%d",
			id, write.Path, write.VolumeName, write.Mode, write.Secret, len(write.Content))
		var writeErr error
		if writer, ok := backend.(ownedFileWriter); ok {
			owner := write.Owner
			if owner == "" {
				owner = defaultOwner
			}
			writeErr = writer.WriteOwnedVolumeFile(ctx, write.VolumeName, write.RelPath, write.Content, write.Mode, owner)
		} else {
			writeErr = backend.WriteVolumeFile(ctx, write.VolumeName, write.RelPath, write.Content, write.Mode)
		}
		if err := writeErr; err != nil {
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
	if reader, ok := backend.(publishedPortReader); ok && len(spec.Ports) > 0 {
		ports, err := reader.PublishedPorts(ctx, containerName, spec.Ports)
		if err != nil {
			return cleanupRuntime(err)
		}
		if err = savePublishedPorts(db, id, ports); err != nil {
			return cleanupRuntime(err)
		}
		spec.Ports = ports
		host := ""
		if runSpecTargetID(spec) > 0 {
			host = remotePublicHost(appCtx, runSpecTargetID(spec))
		}
		url := publicWorkloadURL(host, ports[0])
		if err = updateWorkload(db, id, map[string]any{"public_url": url}); err != nil {
			return cleanupRuntime(err)
		}
	}

	if err := updateWorkload(db, id, map[string]any{"status": StatusRunning, "container_id": cid, "last_error": "", "updated_at": nowUTC()}); err != nil {
		return cleanupRuntime(fmt.Errorf("record running workload: %w", err))
	}
	_ = recordEvent(db, id, "started", "tool", map[string]any{"container_id": cid})
	log.Printf("[containers] runtime probe workload_id=%s", id)
	_ = a.probeWorkloadUnlocked(ctx, appCtx, db, id)
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
	base.DisableHealthCheck = in.DisableHealthCheck
	if in.HealthScheme != "" {
		base.HealthScheme = in.HealthScheme
	}
	if in.HealthPort != 0 {
		base.HealthPort = in.HealthPort
	}
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
	if in.Ports != nil {
		base.Ports = in.Ports
	}
	if in.Env != nil {
		if len(in.Env) == 0 {
			base.Env = map[string]string{}
		}
		if base.Env == nil {
			base.Env = map[string]string{}
		}
		for k, v := range in.Env {
			base.Env[k] = v
		}
	}
	if in.Volumes != nil {
		base.Volumes = in.Volumes
	}
	if in.Files != nil {
		base.Files = in.Files
	}
	if in.PullPolicy != "" {
		base.PullPolicy = in.PullPolicy
	}
	if in.HealthPath != "" {
		base.HealthPath = in.HealthPath
	}
	if in.resourcesPresent || in.Resources.MemoryMB != 0 || in.Resources.CPU != 0 {
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
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM containers_runtime_cleanup WHERE workload_id=?`, id).Scan(&pending); err != nil {
		return nil, err
	}
	if pending > 0 {
		return nil, fmt.Errorf("%w: interrupted creation cleanup is pending; create a new workload", errConflict)
	}
	ctx, unlock, err := a.lockWorkload(ctx, id, false)
	if err != nil {
		return nil, err
	}
	defer unlock()

	w, err := requireWorkload(db, id)
	if err != nil {
		return nil, err
	}
	if w.Status == StatusDestroyed || w.Status == StatusDestroying || w.Status == StatusCreating {
		return nil, errConflict
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
	_ = a.probeWorkloadUnlocked(ctx, appCtx, db, id)
	return getWorkload(db, id)
}

func (a *App) stopWorkload(ctx context.Context, appCtx *sdk.AppCtx, db *sql.DB, id string) (*Workload, error) {
	ctx, unlock, err := a.lockWorkload(ctx, id, false)
	if err != nil {
		return nil, err
	}
	defer unlock()

	w, err := requireWorkload(db, id)
	if err != nil {
		return nil, err
	}
	if w.Status == StatusDestroyed || w.Status == StatusDestroying || w.Status == StatusCreating {
		return nil, errConflict
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
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM containers_runtime_cleanup WHERE workload_id=?`, id).Scan(&pending); err != nil {
		return nil, err
	}
	if pending > 0 {
		return nil, fmt.Errorf("%w: interrupted creation cleanup is pending; create a new workload", errConflict)
	}
	ctx, unlock, err := a.lockWorkload(ctx, id, false)
	if err != nil {
		return nil, err
	}
	defer unlock()

	w, err := requireWorkload(db, id)
	if err != nil {
		return nil, err
	}
	if w.Status == StatusDestroyed || w.Status == StatusDestroying || w.Status == StatusCreating {
		return nil, errConflict
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
	_ = a.probeWorkloadUnlocked(ctx, appCtx, db, id)
	return getWorkload(db, id)
}

func (a *App) destroyWorkload(ctx context.Context, appCtx *sdk.AppCtx, db *sql.DB, id string, deleteVolumes bool) error {
	a.cancelCreation(id)
	ctx, unlock, err := a.lockWorkload(ctx, id, false)
	if err != nil {
		return err
	}
	defer unlock()

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
	if err := updateWorkload(db, id, map[string]any{"status": StatusDestroying, "desired_status": StatusStopped}); err != nil {
		return err
	}
	var cleanupErrs []error
	if err := backend.Remove(ctx, w.ContainerName, true); err != nil && !isDockerMissingResourceError(err, "container") {
		cleanupErrs = append(cleanupErrs, err)
	}
	cleanupErrs = append(cleanupErrs, removeWorkloadExecutionRuntime(ctx, db, backend, w.ID)...)
	if err := backend.RemoveNetwork(ctx, w.NetworkName); err != nil && !isDockerMissingResourceError(err, "network") {
		cleanupErrs = append(cleanupErrs, err)
	}
	if deleteVolumes {
		for _, v := range w.Volumes {
			if err := backend.RemoveVolume(ctx, v.DockerVolumeName); err != nil && !isDockerMissingResourceError(err, "volume") {
				cleanupErrs = append(cleanupErrs, err)
			} else {
				if _, err := db.Exec(`DELETE FROM containers_volumes WHERE workload_id=? AND name=?`, id, v.Name); err != nil {
					cleanupErrs = append(cleanupErrs, err)
				}
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
	ctx, unlock, err := a.lockWorkload(ctx, id, false)
	if err != nil {
		return err
	}
	defer unlock()
	return a.probeWorkloadUnlocked(ctx, appCtx, db, id)
}
func (a *App) probeWorkloadUnlocked(ctx context.Context, appCtx *sdk.AppCtx, db *sql.DB, id string) error {
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM containers_runtime_cleanup WHERE workload_id=?`, id).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return errConflict
	}
	w, err := requireWorkload(db, id)
	if err != nil {
		return err
	}
	if w.Status == StatusDestroyed || w.Status == StatusDestroying || w.Status == StatusCreating {
		return errConflict
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
		var healthSpec RunSpec
		_ = json.Unmarshal([]byte(w.ConfigJSON), &healthSpec)
		if state.Running && !healthSpec.DisableHealthCheck && (healthSpec.HealthPath != "" || w.HealthURL != "") {
			hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			if prober, ok := backend.(workloadHealthProber); ok && healthSpec.HealthPath != "" {
				err = prober.ProbeService(hctx, w, healthSpec)
			} else {
				err = probeHTTP(hctx, w.HealthURL)
			}
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
	project := ""
	if appCtx != nil {
		project = appCtx.CurrentProject()
	}
	rows, err := queryWorkloads(db, "", &project, nil, 0, 0)
	if err != nil {
		return err
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, w := range rows {
		if w.Status == StatusDestroyed || w.Status == StatusDestroying || w.Status == StatusCreating {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		}
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
	if appCtx != nil {
		appCtx = appCtx.WithProject(w.ProjectID)
	}
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
	case "container", "network", "volume":
		return strings.Contains(msg, "no such "+resource) ||
			(strings.Contains(msg, resource+" ") && strings.Contains(msg, "not found"))
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

func (a *App) toolBlueprints(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	bps, err := listBlueprints(ctx.AppDB())
	if err != nil {
		return nil, err
	}
	return map[string]any{"blueprints": bps}, nil
}

// HTTP.

func (a *App) handleWorkloads(w http.ResponseWriter, r *http.Request) {
	appCtx, scopeErr := httpAppContext(r)
	if scopeErr != nil {
		writeResult(w, nil, scopeErr)
		return
	}
	switch r.Method {
	case http.MethodGet:
		project := appCtx.CurrentProject()
		limit := queryInt(r, "limit", 100)
		if limit < 1 || limit > 500 {
			limit = 100
		}
		offset := queryInt(r, "offset", 0)
		if offset < 0 {
			offset = 0
		}
		rows, err := queryWorkloads(appCtx.AppDB(), r.URL.Query().Get("status"), &project, nil, limit, offset, r.URL.Query().Get("retained") == "1")
		writeResult(w, map[string]any{"workloads": rows, "count": len(rows)}, err)
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, maxRunRequestBytes)
		spec, decodeErr := decodeHTTPRun(r)
		if err := decodeErr; err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("[containers] http POST /api/workloads name=%q image=%q blueprint=%q ports=%s volumes=%s env_keys=%s",
			spec.Name, spec.Image, spec.BlueprintSlug, describePorts(spec.Ports), describeVolumes(spec.Volumes), describeEnvKeys(spec.Env))
		res, err := a.queueWorkload(appCtx, appCtx.AppDB(), spec)
		writeResult(w, map[string]any{"workload": res}, err)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleWorkloadItem(w http.ResponseWriter, r *http.Request) {
	appCtx, scopeErr := httpAppContext(r)
	if scopeErr != nil {
		writeResult(w, nil, scopeErr)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/workloads/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "workload id required", http.StatusBadRequest)
		return
	}
	id := parts[0]
	workload, err := requireHTTPWorkload(appCtx, id)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	if a.handleOperatorAction(w, r, appCtx, workload, parts) {
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch {
	case r.Method == http.MethodGet && action == "":
		wk, err := requireWorkload(appCtx.AppDB(), id)
		writeResult(w, map[string]any{"workload": wk}, err)
	case r.Method == http.MethodDelete && action == "":
		err := a.destroyWorkload(r.Context(), appCtx, appCtx.AppDB(), id, r.URL.Query().Get("delete_volumes") == "1")
		writeResult(w, map[string]any{"destroyed": err == nil, "workload_id": id}, err)
	case r.Method == http.MethodPost && action == "start":
		wk, err := a.startWorkload(r.Context(), appCtx, appCtx.AppDB(), id)
		writeResult(w, map[string]any{"workload": wk}, err)
	case r.Method == http.MethodPost && action == "stop":
		wk, err := a.stopWorkload(r.Context(), appCtx, appCtx.AppDB(), id)
		writeResult(w, map[string]any{"workload": wk}, err)
	case r.Method == http.MethodPost && action == "restart":
		wk, err := a.restartWorkload(r.Context(), appCtx, appCtx.AppDB(), id)
		writeResult(w, map[string]any{"workload": wk}, err)
	case r.Method == http.MethodPost && action == "health":
		err := a.probeWorkload(r.Context(), appCtx, appCtx.AppDB(), id)
		wk, _ := getWorkload(appCtx.AppDB(), id)
		writeResult(w, map[string]any{"workload": wk}, err)
	case r.Method == http.MethodGet && action == "usage":
		usage, err := a.workloadUsage(r.Context(), appCtx, appCtx.AppDB(), id)
		if usage == nil {
			writeResult(w, nil, err)
			return
		}
		writeResult(w, map[string]any{"usage": usage, "metrics": usage.Metrics}, err)
	case r.Method == http.MethodGet && action == "logs":
		wk, err := requireWorkload(appCtx.AppDB(), id)
		if err != nil {
			writeResult(w, nil, err)
			return
		}
		backend, err := a.backendForWorkload(appCtx, wk)
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
	appCtx, scopeErr := httpAppContext(r)
	if scopeErr != nil {
		writeResult(w, nil, scopeErr)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	bps, err := listBlueprints(appCtx.AppDB())
	writeResult(w, map[string]any{"blueprints": bps}, err)
}

func (a *App) handleHosts(w http.ResponseWriter, r *http.Request) {
	appCtx, scopeErr := httpAppContext(r)
	if scopeErr != nil {
		writeResult(w, nil, scopeErr)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	hosts, warning := containerHosts(appCtx)
	probeCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	err := a.backend.Probe(probeCtx)
	cancel()
	if err != nil {
		hosts[0].Status = "error"
		warning = err.Error()
	}
	out := map[string]any{
		"hosts":           hosts,
		"default_host_id": defaultHostID(appCtx),
	}
	if warning != "" {
		out["warning"] = warning
	}
	writeResult(w, out, nil)
}

func writeResult(w http.ResponseWriter, v any, err error) {
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errWorkloadNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, errConflict) || strings.Contains(strings.ToLower(err.Error()), "unique") {
			status = http.StatusConflict
		} else if isInputError(err) {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Arg helpers.

func parseRunSpec(args map[string]any) (RunSpec, error) {
	var spec RunSpec
	raw, err := json.Marshal(args)
	if err != nil {
		return spec, err
	}
	if len(raw) > maxRunRequestBytes {
		return spec, errors.New("run request too large")
	}
	if err = json.Unmarshal(raw, &spec); err != nil {
		return spec, fmt.Errorf("invalid run spec: %w", err)
	}
	return spec, nil
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
		"name":           map[string]any{"type": "string"},
		"image":          map[string]any{"type": "string"},
		"blueprint_slug": map[string]any{"type": "string"},
		"host_id":        map[string]any{"type": "integer"},
		"instance_id":    map[string]any{"type": "integer"},
		"use_local":      map[string]any{"type": "boolean"},
		"ports":          map[string]any{"type": "array"},
		"env":            map[string]any{"type": "object"},
		"volumes":        map[string]any{"type": "array", "items": schemaObject(map[string]any{"name": map[string]any{"type": "string"}, "mount_path": map[string]any{"type": "string"}, "retained_from": map[string]any{"type": "string"}}, []string{"name", "mount_path"})},
		"files":          map[string]any{"type": "array", "items": schemaObject(map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}, "content_base64": map[string]any{"type": "string"}, "owner": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string"}, "secret": map[string]any{"type": "boolean"}}, []string{"path"})},
		"pull_policy":    map[string]any{"type": "string", "enum": []string{"missing", "always", "never"}},
		"health_path":    map[string]any{"type": "string"},
		"resources":      schemaObject(map[string]any{"memory_mb": map[string]any{"type": "integer", "minimum": 0}, "cpu": map[string]any{"type": "number", "minimum": 0}}, nil),
		"health_scheme":  map[string]any{"type": "string", "enum": []string{"http", "https"}}, "health_port": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535}, "disable_health_check": map[string]any{"type": "boolean"},
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
		"session_key":       map[string]any{"type": "string", "maxLength": 64},
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
