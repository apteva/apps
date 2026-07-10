package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// MCPTools — agent-facing surface. Each tool's HTTP twin lives in
// handlers.go and shares the underlying logic where possible.
func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "deploy_init", Handler: a.toolInit,
			Description: "Bind a source to a new service, Android, or iOS deployment. Args: name, source_kind, source_ref, target_kind? (service|android|ios), framework?, target_config_json?, build_cmd?, start_cmd?, port_hint?, env_json?, domain?, description?",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":               map[string]any{"type": "string"},
					"source_kind":        map[string]any{"type": "string", "enum": []string{"code", "local"}},
					"source_ref":         map[string]any{"type": "string"},
					"target_kind":        map[string]any{"type": "string", "enum": []string{"service", "android", "ios"}},
					"framework":          map[string]any{"type": "string"},
					"target_config_json": map[string]any{"type": "string"},
					"build_cmd":          map[string]any{"type": "string"},
					"start_cmd":          map[string]any{"type": "string"},
					"port_hint":          map[string]any{"type": "integer"},
					"env_json":           map[string]any{"type": "string"},
					"domain":             map[string]any{"type": "string"},
					"description":        map[string]any{"type": "string"},
				},
				"required": []string{"name", "source_kind", "source_ref"},
			},
		},
		{
			Name: "deploy_list", Handler: a.toolList,
			Description: "List deployments in this project. Args: include_archived?",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"include_archived": map[string]any{"type": "boolean"},
				},
			},
		},
		{
			Name: "deploy_get", Handler: a.toolGet,
			Description: "Full detail for one deployment environment. Args: name OR id, environment? (default production).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string"},
					"id":          map[string]any{"type": "integer"},
					"environment": map[string]any{"type": "string"},
				},
			},
		},
		{
			Name: "deploy_env_list", Handler: a.toolEnvList,
			Description: "List environments for a deployment. Args: name OR id, include_archived?",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":             map[string]any{"type": "string"},
					"id":               map[string]any{"type": "integer"},
					"include_archived": map[string]any{"type": "boolean"},
				},
			},
		},
		{
			Name: "deploy_env_create", Handler: a.toolEnvCreate,
			Description: "Create a deployment environment, copying config from production unless from_environment is supplied. Args: name OR id, environment, from_environment?, source_ref?, env_json?, build_cmd?, start_cmd?, framework?, port_hint?, domain?, description?",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":               map[string]any{"type": "string"},
					"id":                 map[string]any{"type": "integer"},
					"environment":        map[string]any{"type": "string"},
					"from_environment":   map[string]any{"type": "string"},
					"description":        map[string]any{"type": "string"},
					"source_ref":         map[string]any{"type": "string"},
					"source_extra_json":  map[string]any{"type": "string"},
					"framework":          map[string]any{"type": "string"},
					"build_cmd":          map[string]any{"type": "string"},
					"start_cmd":          map[string]any{"type": "string"},
					"port_hint":          map[string]any{"type": "integer"},
					"env_json":           map[string]any{"type": "string"},
					"target_config_json": map[string]any{"type": "string"},
					"domain":             map[string]any{"type": "string"},
				},
				"required": []string{"environment"},
			},
		},
		{
			Name: "deploy_env_update", Handler: a.toolEnvUpdate,
			Description: "Update one environment's config. Args: name OR id, environment, plus mutable fields.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":               map[string]any{"type": "string"},
					"id":                 map[string]any{"type": "integer"},
					"environment":        map[string]any{"type": "string"},
					"description":        map[string]any{"type": "string"},
					"source_ref":         map[string]any{"type": "string"},
					"source_extra_json":  map[string]any{"type": "string"},
					"framework":          map[string]any{"type": "string"},
					"build_cmd":          map[string]any{"type": "string"},
					"start_cmd":          map[string]any{"type": "string"},
					"port_hint":          map[string]any{"type": "integer"},
					"env_json":           map[string]any{"type": "string"},
					"target_config_json": map[string]any{"type": "string"},
				},
				"required": []string{"environment"},
			},
		},
		{
			Name: "deploy_env_destroy", Handler: a.toolEnvDestroy,
			Description: "Archive a non-production environment and stop its live release. Args: name OR id, environment.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string"},
					"id":          map[string]any{"type": "integer"},
					"environment": map[string]any{"type": "string"},
				},
				"required": []string{"environment"},
			},
		},
		{
			Name: "deploy_build", Handler: a.toolBuild,
			Description: "Fetch source and build a service binary/site, Android AAB, or iOS IPA. Args: name OR id, environment?, release?, channel? (mobile auto-release default internal).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":              map[string]any{"type": "string"},
					"id":                map[string]any{"type": "integer"},
					"environment":       map[string]any{"type": "string"},
					"release":           map[string]any{"type": "boolean"},
					"channel":           map[string]any{"type": "string"},
					"rollout_fraction":  map[string]any{"type": "number"},
					"release_notes":     map[string]any{"type": "object"},
					"submit_for_review": map[string]any{"type": "boolean"},
					"beta_group_id":     map[string]any{"type": "string"},
				},
			},
		},
		{
			Name: "deploy_release", Handler: a.toolRelease,
			Description: "Release a build. Services start a process; mobile builds publish to a store channel. Args: build_id, environment?, channel?, rollout_fraction?, release_notes?, submit_for_review?, beta_group_id?",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"build_id":          map[string]any{"type": "integer"},
					"environment":       map[string]any{"type": "string"},
					"channel":           map[string]any{"type": "string"},
					"rollout_fraction":  map[string]any{"type": "number"},
					"release_notes":     map[string]any{"type": "object"},
					"submit_for_review": map[string]any{"type": "boolean"},
					"beta_group_id":     map[string]any{"type": "string"},
				},
				"required": []string{"build_id"},
			},
		},
		{
			Name: "deploy_promote", Handler: a.toolPromote,
			Description: "Promote a tested service build between environments, or a mobile store release between channels without rebuilding/re-uploading. Mobile args: release_id or build_id, target_channel. Service args: source_environment?, target_environment?, build_id?",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":               map[string]any{"type": "string"},
					"id":                 map[string]any{"type": "integer"},
					"source_environment": map[string]any{"type": "string"},
					"target_environment": map[string]any{"type": "string"},
					"build_id":           map[string]any{"type": "integer"},
					"release_id":         map[string]any{"type": "integer"},
					"target_channel":     map[string]any{"type": "string"},
					"rollout_fraction":   map[string]any{"type": "number"},
					"release_notes":      map[string]any{"type": "object"},
					"submit_for_review":  map[string]any{"type": "boolean"},
					"beta_group_id":      map[string]any{"type": "string"},
				},
			},
		},
		{
			Name: "deploy_rollout", Handler: a.toolRollout,
			Description: "Change an Android production staged rollout. Args: release_id, fraction (0 < fraction <= 1).",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"release_id": map[string]any{"type": "integer"}, "fraction": map[string]any{"type": "number"},
			}, "required": []string{"release_id", "fraction"}},
		},
		{
			Name: "deploy_halt", Handler: a.toolHalt,
			Description: "Halt an Android staged rollout or expire an iOS TestFlight build. Args: release_id.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"release_id": map[string]any{"type": "integer"}}, "required": []string{"release_id"}},
		},
		{
			Name: "deploy_status", Handler: a.toolStatus,
			Description: "Current build + release status, URL, last 10 builds. Args: name OR id.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string"},
					"id":          map[string]any{"type": "integer"},
					"environment": map[string]any{"type": "string"},
				},
			},
		},
		{
			Name: "deploy_logs", Handler: a.toolLogs,
			Description: "Tail build or runtime logs. Args: build_id OR release_id, tail? (lines, default 200).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"build_id":   map[string]any{"type": "integer"},
					"release_id": map[string]any{"type": "integer"},
					"tail":       map[string]any{"type": "integer"},
				},
			},
		},
		{
			Name: "deploy_stop", Handler: a.toolStop,
			Description: "Stop the live release. Args: name OR id.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string"},
					"id":          map[string]any{"type": "integer"},
					"environment": map[string]any{"type": "string"},
				},
			},
		},
		{
			Name: "deploy_destroy", Handler: a.toolDestroy,
			Description: "Stop, drop deployment, delete builds and artifacts. Args: name OR id.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string"},
					"id":          map[string]any{"type": "integer"},
					"environment": map[string]any{"type": "string"},
				},
			},
		},
		{
			Name: "deploy_attach_domain", Handler: a.toolAttachDomain,
			Description: "Attach an FQDN to a deployment via the Domains app. Validates the FQDN sits under a registered domain, then upserts a DNS record pointing at the deploy. Target resolves: explicit `target` -> the deploy app's `public_host` config -> the box's own IP (auto-derived from APTEVA_PUBLIC_URL). Record type is inferred from the target -- IP -> A, hostname -> CNAME -- unless `type` is passed. Args: name OR id, fqdn, target?, type? (CNAME|A), ttl?.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string"},
					"id":          map[string]any{"type": "integer"},
					"environment": map[string]any{"type": "string"},
					"fqdn":        map[string]any{"type": "string"},
					"target":      map[string]any{"type": "string"},
					"type":        map[string]any{"type": "string", "enum": []string{"CNAME", "A"}},
					"ttl":         map[string]any{"type": "integer"},
				},
				"required": []string{"fqdn"},
			},
		},
		{
			Name: "deploy_detach_domain", Handler: a.toolDetachDomain,
			Description: "Clear a deployment's domain link. Best-effort deletes the DNS record via the Domains app and clears the deployment's domain field. Args: name OR id.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string"},
					"id":          map[string]any{"type": "integer"},
					"environment": map[string]any{"type": "string"},
				},
			},
		},
		{
			Name: "deploy_list_routes", Handler: a.toolListRoutes,
			Description: "List live deployments as a route table for the host-based proxy. Returns [{slug, port, domain, status}]; only deployments with a current_release in 'live' or 'starting' status are returned. Used by the server, not by agents.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name: "deploy_health", Handler: a.toolHealth,
			Description: "Health snapshot for this project's deployments plus build-artifact retention status. Returns deployments whose current release is crashed/failed, or stuck in starting, or auto-restart paused. Includes how long each has been unhealthy and the auto-restart attempt count. Designed for a jobs cron / alert worker to poll on a tight interval: an empty `unhealthy` list means releases are fine. Args: (none, project_id from context).",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name: "deploy_update", Handler: a.toolUpdate,
			Description: "Mutate deployment config (env_json, build_cmd, start_cmd, port_hint, description, framework, source_extra_json) without delete+recreate. New values take effect on the next build/release; call deploy_restart to apply immediately without rebuilding. Unknown fields are silently ignored — pass only what you want to change. Args: name OR id, plus any subset of the mutable fields.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":               map[string]any{"type": "string"},
					"id":                 map[string]any{"type": "integer"},
					"environment":        map[string]any{"type": "string"},
					"description":        map[string]any{"type": "string"},
					"source_ref":         map[string]any{"type": "string"},
					"framework":          map[string]any{"type": "string"},
					"build_cmd":          map[string]any{"type": "string"},
					"start_cmd":          map[string]any{"type": "string"},
					"port_hint":          map[string]any{"type": "integer"},
					"env_json":           map[string]any{"type": "string"},
					"source_extra_json":  map[string]any{"type": "string"},
					"target_config_json": map[string]any{"type": "string"},
				},
			},
		},
		{
			Name: "deploy_restart", Handler: a.toolRestart,
			Description: "Re-spawn the current release with whatever config the deployment row now holds. Authoritative stop (port guaranteed free), then runRelease against the same build_id — so a config-only change (env_json, port_hint, start_cmd) takes effect without rebuilding. Errors if there is no current release to restart. Args: name OR id.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string"},
					"id":          map[string]any{"type": "integer"},
					"environment": map[string]any{"type": "string"},
				},
			},
		},
		{
			Name: "deploy_set_env", Handler: a.toolSetEnv,
			Description: "Merge-update the deployment's env_json without round-tripping the full blob. Existing keys are preserved; supplied keys overwrite. Pass `restart: true` to apply immediately via deploy_restart. Args: name OR id, env (object of string→string), restart? (default false).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string"},
					"id":          map[string]any{"type": "integer"},
					"environment": map[string]any{"type": "string"},
					"env":         map[string]any{"type": "object"},
					"restart":     map[string]any{"type": "boolean"},
				},
			},
		},
	}
}

// ─── tool handlers ────────────────────────────────────────────────

func (a *App) toolInit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	domainArg := strings.TrimSpace(strArg(args, "domain"))
	// When the Domains app is installed, route the inline `domain` arg
	// through the attach flow (validates ownership, writes DNS). When
	// it isn't, fall through to the historical free-text behavior so
	// installs without Domains still work.
	domainsOn := domainArg != "" && a.domainsAvailable(ctx)
	in := CreateDeploymentInput{
		Name:             strArg(args, "name"),
		TargetKind:       normalizeTargetKind(strArg(args, "target_kind")),
		Description:      strArg(args, "description"),
		SourceKind:       strArg(args, "source_kind"),
		SourceRef:        strArg(args, "source_ref"),
		Framework:        strArg(args, "framework"),
		BuildCmd:         strArg(args, "build_cmd"),
		StartCmd:         strArg(args, "start_cmd"),
		PortHint:         intArg(args, "port_hint"),
		EnvJSON:          strArg(args, "env_json"),
		TargetConfigJSON: strArg(args, "target_config_json"),
	}
	if in.TargetKind != "service" && in.TargetKind != "android" && in.TargetKind != "ios" {
		return nil, fmt.Errorf("target_kind %q not supported (service|android|ios)", in.TargetKind)
	}
	if in.TargetKind == "android" || in.TargetKind == "ios" {
		if in.Framework == "" {
			in.Framework = in.TargetKind
		}
		if in.Framework != in.TargetKind {
			return nil, fmt.Errorf("target_kind %q requires framework %q", in.TargetKind, in.TargetKind)
		}
		if domainArg != "" {
			return nil, errors.New("domains apply to service deployments; put the backend URL in the mobile environment config")
		}
	}
	if !domainsOn {
		in.Domain = domainArg
	}
	if err := validateName(in.Name); err != nil {
		return nil, err
	}
	d, err := dbCreateDeployment(ctx.AppDB(), pid, in)
	if err != nil {
		return nil, err
	}
	env, err := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	if err != nil {
		return nil, err
	}
	effective := effectiveDeploymentForEnvironment(d, env)
	emit("deploy.created", map[string]any{
		"deployment_id": d.ID, "name": d.Name, "source_kind": d.SourceKind,
	})
	if domainsOn {
		attachRes, err := a.attachDomain(ctx, effective, attachDomainSpec{FQDN: domainArg})
		if err != nil {
			// Don't roll back the deployment — the user can fix the
			// domain wiring (or detach) without losing the binding.
			return map[string]any{"deployment": d, "domain_error": err.Error()}, nil
		}
		d, _ = dbGetDeployment(ctx.AppDB(), pid, d.ID)
		return map[string]any{"deployment": d, "attach": attachRes}, nil
	}
	return map[string]any{"deployment": d}, nil
}

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	include := boolArg(args, "include_archived")
	rows, err := dbListDeployments(ctx.AppDB(), pid, include)
	if err != nil {
		return nil, err
	}
	return map[string]any{"deployments": rows, "count": len(rows)}, nil
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	builds, _ := dbListBuildsForEnv(ctx.AppDB(), d.ID, d.EnvironmentID, 10)
	releases, _ := dbListReleasesForEnv(ctx.AppDB(), d.ID, d.EnvironmentID, 10)
	var current *Release
	if d.CurrentReleaseID != nil {
		current, _ = dbGetRelease(ctx.AppDB(), *d.CurrentReleaseID)
	}
	envs, _ := dbListEnvironments(ctx.AppDB(), d.ID, false)
	return map[string]any{
		"deployment":      d,
		"environments":    envs,
		"builds":          builds,
		"releases":        releases,
		"current_release": current,
		"url":             a.deploymentURL(d, current),
	}, nil
}

func (a *App) toolEnvList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupBaseDeployment(args)
	if err != nil {
		return nil, err
	}
	envs, err := dbListEnvironments(ctx.AppDB(), d.ID, boolArg(args, "include_archived"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"deployment": d, "environments": envs, "count": len(envs)}, nil
}

func (a *App) toolEnvCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupBaseDeployment(args)
	if err != nil {
		return nil, err
	}
	name := normalizeEnvironmentName(strArg(args, "environment"))
	if name == "" || name == defaultEnvironmentName {
		return nil, errors.New("environment must be a non-production name")
	}
	fromName := normalizeEnvironmentName(strArg(args, "from_environment"))
	from, err := dbGetEnvironmentByName(ctx.AppDB(), d.ID, fromName)
	if err != nil {
		return nil, err
	}
	if from == nil && fromName == defaultEnvironmentName {
		from, err = dbEnsureProductionEnvironment(ctx.AppDB(), d)
	}
	if err != nil {
		return nil, err
	}
	if from == nil {
		return nil, fmt.Errorf("source environment %q not found", fromName)
	}
	in := CreateEnvironmentInput{
		Name: name, Description: from.Description,
		SourceRef: from.SourceRef, SourceExtraJSON: from.SourceExtraJSON,
		Framework: from.Framework, BuildCmd: from.BuildCmd, StartCmd: from.StartCmd,
		PortHint: from.PortHint, EnvJSON: from.EnvJSON, TargetConfigJSON: from.TargetConfigJSON,
	}
	applyEnvironmentInputOverrides(&in, args)
	env, err := dbCreateEnvironment(ctx.AppDB(), d.ID, in)
	if err != nil {
		return nil, err
	}
	emit("deploy.environment.created", map[string]any{"deployment_id": d.ID, "environment_id": env.ID, "environment": env.Name})
	return map[string]any{"environment": env, "deployment": effectiveDeploymentForEnvironment(d, env)}, nil
}

func (a *App) toolEnvUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, env, err := a.lookupEnvironment(args)
	if err != nil {
		return nil, err
	}
	fields := environmentFieldsFromArgs(args)
	if len(fields) == 0 {
		return nil, errors.New("no mutable fields supplied")
	}
	if err := dbUpdateEnvironment(ctx.AppDB(), env.ID, fields); err != nil {
		return nil, err
	}
	if env.Name == defaultEnvironmentName {
		_ = dbUpdateDeployment(ctx.AppDB(), d.ProjectID, d.ID, fields)
	}
	freshEnv, _ := dbGetEnvironment(ctx.AppDB(), env.ID)
	emit("deploy.environment.updated", map[string]any{"deployment_id": d.ID, "environment_id": env.ID, "environment": env.Name, "fields": keysOf(fields)})
	return map[string]any{"environment": freshEnv, "deployment": effectiveDeploymentForEnvironment(d, freshEnv), "applied": keysOf(fields)}, nil
}

func (a *App) toolEnvDestroy(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, env, err := a.lookupEnvironment(args)
	if err != nil {
		return nil, err
	}
	if env.Name == defaultEnvironmentName {
		return nil, errors.New("production environment cannot be destroyed")
	}
	effective := effectiveDeploymentForEnvironment(d, env)
	if effective.DomainRecordID != "" || effective.Domain != "" {
		_ = a.detachDomain(ctx, effective)
	}
	if err := a.stopRunningReleasesForDeployment(d.ID, env.ID, 5*time.Second); err != nil {
		return nil, err
	}
	if err := dbUpdateEnvironment(ctx.AppDB(), env.ID, map[string]any{"archived_at": nowUTC(), "current_release_id": nil}); err != nil {
		return nil, err
	}
	emit("deploy.environment.destroyed", map[string]any{"deployment_id": d.ID, "environment_id": env.ID, "environment": env.Name})
	return map[string]any{"destroyed": true, "environment": env.Name, "environment_id": env.ID}, nil
}

func (a *App) toolBuild(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	build, err := a.runBuild(d)
	if err != nil {
		return nil, err
	}
	res := map[string]any{"build": build}
	if boolArg(args, "release") && build.Status == "succeeded" {
		rel, err := a.runReleaseWithOptions(d, build, releaseOptionsFromArgs(args))
		if err != nil {
			res["release_error"] = err.Error()
		} else {
			res["release"] = rel
			res["url"] = a.deploymentURL(d, rel)
		}
	}
	return res, nil
}

func (a *App) toolRelease(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	bid := int64(intArg(args, "build_id"))
	if bid == 0 {
		return nil, errors.New("build_id required")
	}
	build, err := dbGetBuild(ctx.AppDB(), bid)
	if err != nil || build == nil {
		return nil, fmt.Errorf("build %d not found", bid)
	}
	d, err := dbGetDeployment(ctx.AppDB(), pid, build.DeploymentID)
	if err != nil || d == nil {
		return nil, errors.New("deployment not found for that build")
	}
	if envName := strArg(args, "environment"); envName != "" {
		env, err := dbGetEnvironmentByName(ctx.AppDB(), d.ID, envName)
		if err != nil || env == nil {
			return nil, fmt.Errorf("environment %q not found", normalizeEnvironmentName(envName))
		}
		d = effectiveDeploymentForEnvironment(d, env)
	} else if build.EnvironmentID > 0 {
		env, err := dbGetEnvironment(ctx.AppDB(), build.EnvironmentID)
		if err != nil || env == nil {
			return nil, errors.New("environment not found for that build")
		}
		d = effectiveDeploymentForEnvironment(d, env)
	} else if env, err := dbEnsureProductionEnvironment(ctx.AppDB(), d); err == nil && env != nil {
		d = effectiveDeploymentForEnvironment(d, env)
	}
	rel, err := a.runReleaseWithOptions(d, build, releaseOptionsFromArgs(args))
	if err != nil {
		return nil, err
	}
	return map[string]any{"release": rel, "url": a.deploymentURL(d, rel)}, nil
}

func (a *App) toolPromote(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	base, err := a.lookupBaseDeployment(args)
	if err != nil {
		return nil, err
	}
	if base.TargetKind == "android" || base.TargetKind == "ios" {
		return a.toolPromoteMobile(ctx, base, args)
	}
	sourceName := normalizeEnvironmentName(defaultStr(strArg(args, "source_environment"), "staging"))
	targetName := normalizeEnvironmentName(defaultStr(strArg(args, "target_environment"), defaultEnvironmentName))
	targetEnv, err := dbGetEnvironmentByName(ctx.AppDB(), base.ID, targetName)
	if err != nil {
		return nil, err
	}
	if targetEnv == nil && targetName == defaultEnvironmentName {
		targetEnv, err = dbEnsureProductionEnvironment(ctx.AppDB(), base)
	}
	if err != nil {
		return nil, err
	}
	if targetEnv == nil {
		return nil, fmt.Errorf("target environment %q not found", targetName)
	}

	var build *Build
	if bid := int64(intArg(args, "build_id")); bid != 0 {
		build, err = dbGetBuild(ctx.AppDB(), bid)
		if err != nil || build == nil || build.DeploymentID != base.ID {
			return nil, fmt.Errorf("build %d not found for deployment", bid)
		}
	} else {
		sourceEnv, err := dbGetEnvironmentByName(ctx.AppDB(), base.ID, sourceName)
		if err != nil || sourceEnv == nil {
			return nil, fmt.Errorf("source environment %q not found", sourceName)
		}
		if sourceEnv.CurrentReleaseID != nil {
			rel, _ := dbGetRelease(ctx.AppDB(), *sourceEnv.CurrentReleaseID)
			if rel != nil {
				build, _ = dbGetBuild(ctx.AppDB(), rel.BuildID)
			}
		}
		if build == nil {
			builds, _ := dbListBuildsForEnv(ctx.AppDB(), base.ID, sourceEnv.ID, 10)
			for i := range builds {
				if builds[i].Status == "succeeded" {
					build = &builds[i]
					break
				}
			}
		}
	}
	if build == nil {
		return nil, errors.New("no succeeded source build found to promote")
	}
	target := effectiveDeploymentForEnvironment(base, targetEnv)
	rel, err := a.runRelease(target, build)
	if err != nil {
		return nil, err
	}
	emit("deploy.promoted", map[string]any{
		"deployment_id": base.ID, "build_id": build.ID,
		"source_environment": sourceName, "target_environment": targetName,
		"release_id": rel.ID,
	})
	return map[string]any{"build": build, "release": rel, "deployment": target, "url": a.deploymentURL(target, rel)}, nil
}

func (a *App) toolStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	builds, _ := dbListBuildsForEnv(ctx.AppDB(), d.ID, d.EnvironmentID, 10)
	releases, _ := dbListReleasesForEnv(ctx.AppDB(), d.ID, d.EnvironmentID, 20)
	var current *Release
	if d.CurrentReleaseID != nil {
		current, _ = dbGetRelease(ctx.AppDB(), *d.CurrentReleaseID)
	}
	return map[string]any{
		"deployment":      d,
		"builds":          builds,
		"releases":        releases,
		"current_release": current,
		"url":             a.deploymentURL(d, current),
	}, nil
}

func (a *App) toolLogs(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tail := intArg(args, "tail")
	if tail == 0 {
		tail = 200
	}
	if bid := int64(intArg(args, "build_id")); bid != 0 {
		b, err := dbGetBuild(ctx.AppDB(), bid)
		if err != nil || b == nil {
			return nil, fmt.Errorf("build %d not found", bid)
		}
		body, err := tailFile(b.LogPath, tail)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "build", "build_id": bid, "log": body}, nil
	}
	if rid := int64(intArg(args, "release_id")); rid != 0 {
		r, err := dbGetRelease(ctx.AppDB(), rid)
		if err != nil || r == nil {
			return nil, fmt.Errorf("release %d not found", rid)
		}
		body, err := tailFile(r.LogPath, tail)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "release", "release_id": rid, "log": body}, nil
	}
	return nil, errors.New("build_id or release_id required")
}

func (a *App) toolStop(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	if d.CurrentReleaseID == nil {
		return map[string]any{"stopped": false, "reason": "no live release"}, nil
	}
	rid := *d.CurrentReleaseID
	rel, _ := dbGetRelease(ctx.AppDB(), rid)
	// Authoritative stop — see stopReleaseAuthoritative comment.
	if err := a.stopReleaseAuthoritative(rel, 5*time.Second); err != nil {
		return nil, err
	}
	a.markStopped(rid)
	if d.EnvironmentID > 0 {
		_ = dbSetEnvironmentCurrentRelease(ctx.AppDB(), d.EnvironmentID, nil)
	} else {
		_ = dbSetCurrentRelease(ctx.AppDB(), d.ID, nil)
	}
	return map[string]any{"stopped": true, "release_id": rid}, nil
}

func (a *App) toolDestroy(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if envName := normalizeEnvironmentName(strArg(args, "environment")); envName != defaultEnvironmentName {
		return a.toolEnvDestroy(ctx, args)
	}
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	// Drop the DNS record before deleting the row so the deployment's
	// link metadata is still around for detach to work.
	if d.DomainRecordID != "" {
		_ = a.detachDomain(ctx, d)
	}
	// Stop every live/starting release across every environment first
	// so deleting the DB row cannot strand an environment process.
	if err := a.stopRunningReleasesForDeployment(d.ID, 0, 5*time.Second); err != nil {
		return nil, err
	}
	// Capture build rows before the CASCADE wipes them so artifact
	// directories can still be deleted from disk.
	builds, _ := dbListBuilds(ctx.AppDB(), d.ID, 100000)
	// Delete the row (CASCADE wipes builds + releases + events + leases).
	if err := dbDeleteDeployment(ctx.AppDB(), pid, d.ID); err != nil {
		return nil, err
	}
	a.removeBuildDirs(builds)
	emit("deploy.destroyed", map[string]any{"deployment_id": d.ID, "name": d.Name})
	return map[string]any{"destroyed": true, "id": d.ID}, nil
}

func (a *App) toolAttachDomain(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	if d.TargetKind != "service" {
		return nil, errors.New("domains apply to service deployments, not mobile binaries")
	}
	spec := attachDomainSpec{
		FQDN:   strArg(args, "fqdn"),
		Target: strArg(args, "target"),
		Type:   strArg(args, "type"),
		TTL:    intArg(args, "ttl"),
	}
	attachRes, err := a.attachDomain(ctx, d, spec)
	if err != nil {
		return nil, err
	}
	pid, _ := resolveProjectFromArgs(args)
	out := d
	if d.EnvironmentID > 0 {
		base, _ := dbGetDeployment(ctx.AppDB(), pid, d.ID)
		env, _ := dbGetEnvironment(ctx.AppDB(), d.EnvironmentID)
		if base != nil && env != nil {
			out = effectiveDeploymentForEnvironment(base, env)
		}
	} else {
		out, _ = dbGetDeployment(ctx.AppDB(), pid, d.ID)
	}
	return map[string]any{"deployment": out, "attach": attachRes}, nil
}

func (a *App) toolDetachDomain(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	res := map[string]any{"detached": true, "id": d.ID, "fqdn": d.Domain}
	if err := a.detachDomain(ctx, d); err != nil {
		// Domain row was cleared either way; surface the registrar
		// error so the user can clean it up manually if needed.
		res["registrar_error"] = err.Error()
	}
	return res, nil
}

type RouteEntry struct {
	Slug      string `json:"slug"`
	ProjectID string `json:"project_id,omitempty"`
	Port      int    `json:"port"`
	Domain    string `json:"domain,omitempty"`
	Status    string `json:"status"`
}

// toolListRoutes is the server's pull-side: a small, no-secrets shape
// it can refresh into its route table on a 5-second tick.
func (a *App) toolListRoutes(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	releases, err := dbListLiveReleases(ctx.AppDB())
	if err != nil {
		return nil, err
	}
	out := make([]RouteEntry, 0, len(releases))
	for _, r := range releases {
		d, err := a.deploymentForRelease(&r)
		if err != nil || d == nil {
			continue
		}
		out = append(out, RouteEntry{
			Slug: d.Name, ProjectID: d.ProjectID, Port: r.Port,
			Domain: d.Domain, Status: r.Status,
		})
	}
	return map[string]any{"routes": out, "count": len(out)}, nil
}

// toolHealth returns a snapshot of unhealthy deployments for polling
// alert sinks (jobs cron, watchman). A deployment is unhealthy if
// any of: current_release is crashed/failed, current release is
// stuck in starting > 2min, or auto-restart is paused.
//
// Returns an empty `unhealthy` list when everything is fine — the
// caller's alert rule is "len(unhealthy) > 0 → page". Designed to be
// cheap to poll: a single dbListDeployments + one dbGetRelease per
// deployment.
func (a *App) toolHealth(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	deps, err := dbListDeployments(ctx.AppDB(), pid, false /* include archived */)
	if err != nil {
		return nil, err
	}
	type unhealthyEntry struct {
		DeploymentID  int64           `json:"deployment_id"`
		EnvironmentID int64           `json:"environment_id,omitempty"`
		Environment   string          `json:"environment,omitempty"`
		Name          string          `json:"name"`
		Domain        string          `json:"domain,omitempty"`
		Status        string          `json:"status"` // crashed | failed | starting_stuck | auto_restart_paused
		ReleaseID     int64           `json:"release_id,omitempty"`
		Reason        string          `json:"reason,omitempty"`
		UnhealthyForS int             `json:"unhealthy_for_s"` // seconds since the bad state began
		AutoRestart   autoRestartInfo `json:"auto_restart"`
	}
	out := []unhealthyEntry{}
	now := time.Now().UTC()
	for _, d := range deps {
		envs, err := dbListEnvironments(ctx.AppDB(), d.ID, false)
		if err != nil || len(envs) == 0 {
			if env, err := dbEnsureProductionEnvironment(ctx.AppDB(), &d); err == nil && env != nil {
				envs = []DeploymentEnvironment{*env}
			}
		}
		for _, env := range envs {
			effective := effectiveDeploymentForEnvironment(&d, &env)
			a.autoRestartMu.Lock()
			ar := a.autoRestartState[autoRestartStateKey(d.ID, env.ID)]
			a.autoRestartMu.Unlock()
			if ar.Paused {
				out = append(out, unhealthyEntry{
					DeploymentID: d.ID, EnvironmentID: env.ID, Environment: env.Name,
					Name: d.Name, Domain: effective.Domain,
					Status: "auto_restart_paused", Reason: "max attempts reached",
					AutoRestart: ar,
				})
				continue
			}
			if effective.CurrentReleaseID == nil {
				continue // no release ever → not unhealthy, just unbooted
			}
			rel, _ := dbGetRelease(ctx.AppDB(), *effective.CurrentReleaseID)
			if rel == nil {
				continue
			}
			var entry *unhealthyEntry
			switch rel.Status {
			case "crashed", "failed":
				since := 0
				if t, err := time.Parse(time.RFC3339, rel.StoppedAt); err == nil {
					since = int(now.Sub(t).Seconds())
				}
				entry = &unhealthyEntry{
					DeploymentID: d.ID, EnvironmentID: env.ID, Environment: env.Name,
					Name: d.Name, Domain: effective.Domain,
					Status: rel.Status, ReleaseID: rel.ID,
					Reason: rel.Error, UnhealthyForS: since,
					AutoRestart: ar,
				}
			case "starting":
				startedAt, _ := time.Parse(time.RFC3339, rel.StartedAt)
				if !startedAt.IsZero() && now.Sub(startedAt) > 2*time.Minute {
					entry = &unhealthyEntry{
						DeploymentID: d.ID, EnvironmentID: env.ID, Environment: env.Name,
						Name: d.Name, Domain: effective.Domain,
						Status: "starting_stuck", ReleaseID: rel.ID,
						Reason:        "release in starting state > 2min — pid never owned port",
						UnhealthyForS: int(now.Sub(startedAt).Seconds()),
						AutoRestart:   ar,
					}
				}
			}
			if entry != nil {
				out = append(out, *entry)
			}
		}
	}
	retention, _ := a.retentionStatus(ctx.AppDB())
	return map[string]any{
		"unhealthy":  out,
		"count":      len(out),
		"retention":  retention,
		"checked_at": now,
	}, nil
}

// toolUpdate is the MCP twin of PATCH /api/deployments/:name. Reads
// the mutable-field allowlist from args and writes them. No-restart
// — operator calls deploy_restart explicitly to apply.
func (a *App) toolUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	fields := environmentFieldsFromArgs(args)
	if len(fields) == 0 {
		return nil, errors.New("no mutable fields supplied (allowed: description, framework, build_cmd, start_cmd, port_hint, env_json, source_extra_json)")
	}
	if d.EnvironmentID > 0 {
		if err := dbUpdateEnvironment(ctx.AppDB(), d.EnvironmentID, fields); err != nil {
			return nil, err
		}
		if d.EnvironmentName == defaultEnvironmentName {
			_ = dbUpdateDeployment(ctx.AppDB(), d.ProjectID, d.ID, fields)
		}
	} else if err := dbUpdateDeployment(ctx.AppDB(), d.ProjectID, d.ID, fields); err != nil {
		return nil, err
	}
	emit("deploy.updated", map[string]any{
		"deployment_id": d.ID, "name": d.Name,
		"fields": keysOf(fields),
	})
	fresh, _ := a.lookupDeployment(args)
	return map[string]any{
		"deployment": fresh,
		"applied":    keysOf(fields),
		"note":       "new values apply on the next build/release; call deploy_restart to apply now without rebuilding",
	}, nil
}

// toolRestart is the MCP twin of POST /api/deployments/:name/restart.
// Re-spawns the current release with whatever config the deployment
// row holds RIGHT NOW (post-update). No new build.
func (a *App) toolRestart(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	if d.CurrentReleaseID == nil {
		return nil, errors.New("no current release to restart — call deploy_release (or deploy_build with release=true) first")
	}
	rel, err := dbGetRelease(ctx.AppDB(), *d.CurrentReleaseID)
	if err != nil || rel == nil {
		return nil, errors.New("current release missing")
	}
	build, err := dbGetBuild(ctx.AppDB(), rel.BuildID)
	if err != nil || build == nil {
		return nil, errors.New("build for current release missing")
	}
	if err := a.stopReleaseAuthoritative(rel, 5*time.Second); err != nil {
		return nil, fmt.Errorf("stop: %w", err)
	}
	a.markStopped(rel.ID)
	fresh, _ := a.lookupDeployment(args)
	if fresh == nil {
		fresh = d
	}
	newRel, err := a.runRelease(fresh, build)
	if err != nil {
		return nil, fmt.Errorf("release: %w", err)
	}
	emit("deploy.restarted", map[string]any{
		"deployment_id": d.ID, "release_id": newRel.ID, "build_id": build.ID,
	})
	return map[string]any{
		"release": newRel,
		"url":     a.deploymentURL(fresh, newRel),
	}, nil
}

// toolSetEnv merges the `env` arg into the deployment's existing
// env_json. Existing keys not in `env` are preserved; supplied keys
// overwrite. To CLEAR a key, pass it with an empty-string value
// (or update the full env_json via deploy_update). Optional restart
// flag triggers a re-release with the merged env.
func (a *App) toolSetEnv(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	envArg, _ := args["env"].(map[string]any)
	if len(envArg) == 0 {
		return nil, errors.New("env required (object of string→string)")
	}
	// Merge: start from existing env_json, overlay supplied keys.
	cur := map[string]string{}
	if d.EnvJSON != "" {
		// Best-effort decode; if env_json was hand-written invalid, treat
		// as empty and let the merge overwrite with the new shape.
		_ = json.Unmarshal([]byte(d.EnvJSON), &cur)
	}
	for k, v := range envArg {
		if s, ok := v.(string); ok {
			cur[k] = s
		}
	}
	merged, err := json.Marshal(cur)
	if err != nil {
		return nil, err
	}
	if d.EnvironmentID > 0 {
		if err := dbUpdateEnvironment(ctx.AppDB(), d.EnvironmentID, map[string]any{"env_json": string(merged)}); err != nil {
			return nil, err
		}
		if d.EnvironmentName == defaultEnvironmentName {
			_ = dbUpdateDeployment(ctx.AppDB(), d.ProjectID, d.ID, map[string]any{"env_json": string(merged)})
		}
	} else if err := dbUpdateDeployment(ctx.AppDB(), d.ProjectID, d.ID, map[string]any{
		"env_json": string(merged),
	}); err != nil {
		return nil, err
	}
	emit("deploy.env_updated", map[string]any{
		"deployment_id": d.ID, "name": d.Name, "keys": keysOfMap(envArg),
	})
	envKeys := make([]string, 0, len(cur))
	for k := range cur {
		envKeys = append(envKeys, k)
	}
	res := map[string]any{
		"deployment_id": d.ID,
		"env_keys":      envKeys,
	}
	if restart, _ := args["restart"].(bool); restart && d.CurrentReleaseID != nil {
		restartRes, rerr := a.toolRestart(ctx, args)
		if rerr != nil {
			res["restart_error"] = rerr.Error()
		} else {
			res["restart"] = restartRes
		}
	} else {
		res["note"] = "env updated; current release still running with the old env. Pass restart=true to apply now, or call deploy_restart later."
	}
	return res, nil
}

func keysOfMap(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ─── helpers ──────────────────────────────────────────────────────

func (a *App) lookupDeployment(args map[string]any) (*Deployment, error) {
	d, err := a.lookupBaseDeployment(args)
	if err != nil {
		return nil, err
	}
	envName := normalizeEnvironmentName(strArg(args, "environment"))
	env, err := dbGetEnvironmentByName(globalCtx.AppDB(), d.ID, envName)
	if err != nil {
		return nil, err
	}
	if env == nil && envName == defaultEnvironmentName {
		env, err = dbEnsureProductionEnvironment(globalCtx.AppDB(), d)
		if err != nil {
			return nil, err
		}
	}
	if env == nil {
		return nil, fmt.Errorf("environment %q not found", envName)
	}
	return effectiveDeploymentForEnvironment(d, env), nil
}

func (a *App) lookupBaseDeployment(args map[string]any) (*Deployment, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if id := int64(intArg(args, "id")); id != 0 {
		d, err := dbGetDeployment(globalCtx.AppDB(), pid, id)
		if err != nil || d == nil {
			return nil, fmt.Errorf("deployment %d not found", id)
		}
		return d, nil
	}
	if name := strArg(args, "name"); name != "" {
		d, err := dbGetDeploymentByName(globalCtx.AppDB(), pid, name)
		if err != nil {
			return nil, err
		}
		if d == nil {
			return nil, fmt.Errorf("deployment %q not found", name)
		}
		return d, nil
	}
	return nil, errors.New("name or id required")
}

func (a *App) lookupEnvironment(args map[string]any) (*Deployment, *DeploymentEnvironment, error) {
	d, err := a.lookupBaseDeployment(args)
	if err != nil {
		return nil, nil, err
	}
	envName := normalizeEnvironmentName(strArg(args, "environment"))
	env, err := dbGetEnvironmentByName(globalCtx.AppDB(), d.ID, envName)
	if err != nil {
		return nil, nil, err
	}
	if env == nil && envName == defaultEnvironmentName {
		env, err = dbEnsureProductionEnvironment(globalCtx.AppDB(), d)
	}
	if err != nil {
		return nil, nil, err
	}
	if env == nil {
		return nil, nil, fmt.Errorf("environment %q not found", envName)
	}
	return d, env, nil
}

func applyEnvironmentInputOverrides(in *CreateEnvironmentInput, args map[string]any) {
	if v, ok := args["description"].(string); ok {
		in.Description = v
	}
	if v, ok := args["source_ref"].(string); ok {
		in.SourceRef = v
	}
	if v, ok := args["source_extra_json"].(string); ok {
		in.SourceExtraJSON = v
	}
	if v, ok := args["framework"].(string); ok {
		in.Framework = v
	}
	if v, ok := args["build_cmd"].(string); ok {
		in.BuildCmd = v
	}
	if v, ok := args["start_cmd"].(string); ok {
		in.StartCmd = v
	}
	if v, ok := args["env_json"].(string); ok {
		in.EnvJSON = v
	}
	if v, ok := args["target_config_json"].(string); ok {
		in.TargetConfigJSON = v
	}
	if v, ok := args["domain"].(string); ok {
		in.Domain = v
	}
	if v, ok := args["port_hint"]; ok {
		switch n := v.(type) {
		case float64:
			in.PortHint = int(n)
		case int:
			in.PortHint = n
		}
	}
}

func environmentFieldsFromArgs(args map[string]any) map[string]any {
	fields := map[string]any{}
	for _, k := range []string{
		"description", "source_ref", "source_extra_json",
		"framework", "build_cmd", "start_cmd", "env_json", "target_config_json",
	} {
		if v, ok := args[k].(string); ok {
			fields[k] = v
		}
	}
	if v, ok := args["port_hint"]; ok {
		switch n := v.(type) {
		case float64:
			fields["port_hint"] = int(n)
		case int:
			fields["port_hint"] = n
		}
	}
	return fields
}

func normalizeTargetKind(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "service"
	}
	return value
}

func (a *App) deploymentURL(d *Deployment, current *Release) string {
	if d.Domain != "" {
		return "https://" + d.Domain + "/"
	}
	if current == nil || current.Status != "live" {
		return ""
	}
	return fmt.Sprintf("http://localhost:%d/", current.Port)
}

// tailFile returns the last n lines of a log file. Cheap O(file
// size) read since logs are bounded; replace with reverse-seek when
// they grow.
func tailFile(path string, n int) (string, error) {
	if path == "" {
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return tailLines(string(body), n), nil
}

func tailLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	count := 0
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			count++
			if count > n {
				return s[i+1:]
			}
		}
	}
	return s
}

// ─── arg helpers ──────────────────────────────────────────────────

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func intArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

func boolArg(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}
