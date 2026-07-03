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
	ctx.Logger().Info("containers mounted", "data_dir", ctx.DataDir())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "health-poll",
		Schedule: "@every 30s",
		Run: func(ctx context.Context, app *sdk.AppCtx) error {
			return a.pollHealth(ctx, app, app.AppDB())
		},
	}}
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/api/workloads", Handler: a.handleWorkloads},
		{Pattern: "/api/workloads/", Handler: a.handleWorkloadItem},
		{Pattern: "/api/blueprints", Handler: a.handleBlueprints},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "containers_run", Description: "Run a Docker image as a managed workload.", InputSchema: runSchema(), Handler: a.toolRun},
		{Name: "containers_create", Description: "Alias of containers_run.", InputSchema: runSchema(), Handler: a.toolRun},
		{Name: "containers_get", Description: "Fetch one workload.", InputSchema: idSchema(), Handler: a.toolGet},
		{Name: "containers_list", Description: "List workloads.", InputSchema: schemaObject(map[string]any{"status": map[string]any{"type": "string"}}, nil), Handler: a.toolList},
		{Name: "containers_start", Description: "Start a stopped workload.", InputSchema: idSchema(), Handler: a.toolStart},
		{Name: "containers_stop", Description: "Stop a running workload.", InputSchema: idSchema(), Handler: a.toolStop},
		{Name: "containers_restart", Description: "Restart a workload.", InputSchema: idSchema(), Handler: a.toolRestart},
		{Name: "containers_destroy", Description: "Destroy a workload.", InputSchema: schemaObject(map[string]any{"workload_id": map[string]any{"type": "string"}, "delete_volumes": map[string]any{"type": "boolean"}}, []string{"workload_id"}), Handler: a.toolDestroy},
		{Name: "containers_logs", Description: "Tail workload logs.", InputSchema: schemaObject(map[string]any{"workload_id": map[string]any{"type": "string"}, "tail": map[string]any{"type": "integer"}}, []string{"workload_id"}), Handler: a.toolLogs},
		{Name: "containers_health", Description: "Probe workload health.", InputSchema: idSchema(), Handler: a.toolHealth},
		{Name: "containers_usage_get", Description: "Measure generic workload usage metrics such as container volume storage bytes.", InputSchema: idSchema(), Handler: a.toolUsageGet},
		{Name: "containers_blueprints_list", Description: "List blueprints.", InputSchema: schemaObject(nil, nil), Handler: a.toolBlueprints},
	}
}

func main() { sdk.Run(&App{}) }

func (a *App) createWorkload(ctx context.Context, appCtx *sdk.AppCtx, db *sql.DB, in RunSpec) (*Workload, error) {
	w, spec, err := a.prepareWorkload(appCtx, db, in)
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
	w, spec, err := a.prepareWorkload(appCtx, db, in)
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
	log.Printf("[containers] prepare begin name=%q image=%q blueprint=%q ports=%s volumes=%s env_keys=%s",
		in.Name, in.Image, in.BlueprintSlug, describePorts(in.Ports), describeVolumes(in.Volumes), describeEnvKeys(in.Env))
	spec, err := a.expandBlueprint(db, in)
	if err != nil {
		log.Printf("[containers] prepare expand failed name=%q err=%q", in.Name, err.Error())
		return nil, spec, err
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
	containerName := "containers-" + dockerSafeName(spec.Name)
	networkName := containerName
	for i := range spec.Volumes {
		spec.Volumes[i].DockerVolumeName = containerName + "-" + spec.Volumes[i].Name
	}
	targetID := runSpecTargetID(spec)
	w := &Workload{
		ID: id, Name: spec.Name, BlueprintSlug: spec.BlueprintSlug, HostID: targetID, InstanceID: targetID,
		Kind: "container", Image: spec.Image, Status: StatusCreating, DesiredStatus: StatusRunning,
		ContainerName: containerName, NetworkName: networkName, HealthStatus: "unknown",
		HealthPath: spec.HealthPath, ConfigJSON: encodeJSON(spec), Env: spec.Env,
		EnvJSON: encodeJSON(spec.Env), Resources: spec.Resources, ResourcesJSON: encodeJSON(spec.Resources),
		RestartPolicy: spec.RestartPolicy,
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
	log.Printf("[containers] runtime create_network workload_id=%s network=%q", id, networkName)
	if err := backend.CreateNetwork(ctx, networkName); err != nil {
		return fail(err)
	}
	for _, v := range spec.Volumes {
		log.Printf("[containers] runtime create_volume workload_id=%s volume=%q mount=%q", id, v.DockerVolumeName, v.MountPath)
		if err := backend.CreateVolume(ctx, v.DockerVolumeName); err != nil {
			return fail(err)
		}
	}
	log.Printf("[containers] runtime docker_run workload_id=%s container=%q image=%q", id, containerName, spec.Image)
	cid, err := backend.Run(ctx, spec, containerName, networkName)
	if err != nil {
		return fail(err)
	}
	log.Printf("[containers] runtime docker_run ok workload_id=%s container_id=%s", id, cid)
	_ = updateWorkload(db, id, map[string]any{"status": StatusRunning, "container_id": cid, "last_error": "", "updated_at": nowUTC()})
	_ = recordEvent(db, id, "started", "tool", map[string]any{"container_id": cid})
	log.Printf("[containers] runtime probe workload_id=%s", id)
	_ = a.probeWorkload(ctx, appCtx, db, id)
	log.Printf("[containers] runtime done workload_id=%s duration=%s", id, time.Since(start).Round(time.Millisecond))
	return nil
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
	if in.HealthPath != "" {
		base.HealthPath = in.HealthPath
	}
	if in.Resources.MemoryMB != 0 || in.Resources.CPU != 0 {
		base.Resources = in.Resources
	}
	if in.RestartPolicy != "" {
		base.RestartPolicy = in.RestartPolicy
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
	backend, err := a.backendForWorkload(appCtx, w)
	if err != nil {
		return err
	}
	var cleanupErrs []error
	if err := backend.Remove(ctx, w.ContainerName, true); err != nil && !isDockerMissingResourceError(err, "container") {
		cleanupErrs = append(cleanupErrs, err)
	}
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
	for _, w := range rows {
		if w.Status == StatusDestroyed {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = a.probeWorkload(ctx, appCtx, db, w.ID)
	}
	return nil
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
	return RemoteDocker{app: appCtx, instanceID: targetID}, nil
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
	w, err := requireWorkload(ctx.AppDB(), getStr(args, "workload_id"))
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
	w, err := a.startWorkload(cctx, ctx, ctx.AppDB(), getStr(args, "workload_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload": w}, nil
}

func (a *App) toolStop(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w, err := a.stopWorkload(cctx, ctx, ctx.AppDB(), getStr(args, "workload_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload": w}, nil
}

func (a *App) toolRestart(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	w, err := a.restartWorkload(cctx, ctx, ctx.AppDB(), getStr(args, "workload_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload": w}, nil
}

func (a *App) toolDestroy(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	id := getStr(args, "workload_id")
	if err := a.destroyWorkload(cctx, ctx, ctx.AppDB(), id, boolArg(args, "delete_volumes")); err != nil {
		return nil, err
	}
	return map[string]any{"destroyed": true, "workload_id": id}, nil
}

func (a *App) toolLogs(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	w, err := requireWorkload(ctx.AppDB(), getStr(args, "workload_id"))
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
	id := getStr(args, "workload_id")
	if err := a.probeWorkload(cctx, ctx, ctx.AppDB(), id); err != nil {
		return nil, err
	}
	w, _ := getWorkload(ctx.AppDB(), id)
	return map[string]any{"workload": w}, nil
}

func (a *App) toolUsageGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	usage, err := a.workloadUsage(cctx, ctx, ctx.AppDB(), getStr(args, "workload_id"))
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
	return schemaObject(map[string]any{"workload_id": map[string]any{"type": "string"}}, []string{"workload_id"})
}

func runSchema() map[string]any {
	return schemaObject(map[string]any{
		"name":           map[string]any{"type": "string"},
		"image":          map[string]any{"type": "string"},
		"blueprint_slug": map[string]any{"type": "string"},
		"host_id":        map[string]any{"type": "integer"},
		"instance_id":    map[string]any{"type": "integer"},
		"ports":          map[string]any{"type": "array"},
		"env":            map[string]any{"type": "object"},
		"volumes":        map[string]any{"type": "array"},
		"health_path":    map[string]any{"type": "string"},
		"resources":      map[string]any{"type": "object"},
		"restart_policy": map[string]any{"type": "string"},
	}, []string{"name"})
}
