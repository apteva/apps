package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// toolMigrate moves a Fleet-managed tenant between any supported
// Fleet host: parent local host (instance_id=0) or an Instances VPS.
// The move is cold: stop source, transfer data, start target, commit
// row/routes, then either retain or delete the old data dir. On failure before
// commit, Fleet restarts the original source and leaves the row unchanged.
func (a *App) toolMigrate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := strings.TrimSpace(getStr(args, "tenant_id"))
	if id == "" {
		return nil, errors.New("tenant_id required")
	}
	rawTarget, ok := args["instance_id"]
	if !ok {
		return nil, errors.New("instance_id required (0 = local parent host, >0 = Instances VPS)")
	}
	targetID := int64Arg(map[string]any{"instance_id": rawTarget}, "instance_id")
	t, _, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	done, err := a.beginTenantOperation(t.ID, "migrate")
	if err != nil {
		return nil, err
	}
	defer done()
	if t.Kind != KindLocal {
		return nil, fmt.Errorf("only Fleet-managed local-kind tenants can be moved (this one is %q)", t.Kind)
	}
	sourceHost, err := a.resolveFleetHost(ctx, t.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("source host: %w", err)
	}
	targetHost, err := a.resolveFleetHost(ctx, targetID)
	if err != nil {
		return nil, fmt.Errorf("target host: %w", err)
	}
	sourceDir := t.ConfigDir
	if sourceDir == "" {
		sourceDir, err = tenantDataDirForHost(t.Slug, sourceHost)
		if err != nil {
			return nil, err
		}
	}
	sourcePort, _ := portFromBaseURL(t.BaseURL)
	portOverride := intArg(args, "port", 0)
	retainSource := boolArg(args, "retain_source")
	if t.InstanceID == targetID && (portOverride == 0 || portOverride == sourcePort) {
		return map[string]any{"tenant": a.publicTenantView(t), "migrated": false, "note": "tenant is already on that host"}, nil
	}
	if t.InstanceID == targetID && retainSource {
		return nil, errors.New("retain_source is only supported for cross-host migrations")
	}
	if t.InstanceID != targetID {
		retained, err := a.store.getRetainedSource(t.ID)
		if err != nil {
			return nil, fmt.Errorf("check retained source: %w", err)
		}
		if retained != nil {
			return nil, fmt.Errorf("tenant has a retained migration source from instance %d; finalize it before another cross-host migration", retained.SourceInstanceID)
		}
	}
	targetDir, err := tenantDataDirForHost(t.Slug, targetHost)
	if err != nil {
		return nil, err
	}
	targetPort, err := a.pickTenantPort(ctx, targetHost, portOverride)
	if err != nil {
		return nil, err
	}
	// Complete every remote prerequisite before stopping the source. A
	// package, file-stream, disk, or tunnel failure must leave the running
	// production tenant completely untouched.
	if !sourceHost.IsLocal() {
		if err := a.ensureHostedRuntime(ctx, sourceHost.InstanceID); err != nil {
			return nil, fmt.Errorf("source host preflight: %w", err)
		}
	}
	if !targetHost.IsLocal() {
		if err := a.ensureHostedRuntime(ctx, targetHost.InstanceID); err != nil {
			return nil, fmt.Errorf("target host preflight: %w", err)
		}
	}
	version := tenantVersion(t)
	prevStatus := t.Status

	_ = a.store.recordEvent(t.ID, "migrate_start", "user", map[string]any{
		"from_instance_id": t.InstanceID,
		"to_instance_id":   targetID,
		"from_port":        sourcePort,
		"to_port":          targetPort,
		"retain_source":    retainSource,
	})

	if err := a.stopTenantOnHost(ctx, t, sourcePort); err != nil {
		return nil, fmt.Errorf("stop source tenant: %w", err)
	}
	rollback := func(stage string, cause error) (any, error) {
		_ = a.store.recordEvent(t.ID, "migrate_failed", "user",
			map[string]any{"stage": stage, "error": cause.Error()})
		if baseURL, status, rerr := a.startTenantOnHost(ctx, sourceHost, t.ID, t.Slug, sourceDir, version, sourcePort, prevStatus); rerr == nil {
			_ = a.store.setStatus(t.ID, status, "user")
			_ = a.store.recordEvent(t.ID, "migrate_rolled_back", "user",
				map[string]any{"base_url": baseURL, "port": sourcePort, "instance_id": t.InstanceID})
		} else {
			_ = a.store.setStatus(t.ID, StatusFailed, "user")
			_ = a.store.recordEvent(t.ID, "migrate_rollback_failed", "user",
				map[string]any{"error": rerr.Error()})
		}
		return nil, fmt.Errorf("migrate %s: %w (source tenant restart attempted)", stage, cause)
	}
	if t.InstanceID == targetID {
		baseURL, newStatus, err := a.startTenantOnHost(ctx, sourceHost, t.ID, t.Slug, sourceDir, version, targetPort, prevStatus)
		if err != nil {
			return rollback("restart on new port", err)
		}
		if t.Domain != "" {
			routeTenant := *t
			routeTenant.BaseURL = baseURL
			if err := a.registerRouteForTenantHost(ctx, &routeTenant, t.Domain, t.Domain, "tool:tenant_migrate"); err != nil {
				_ = a.stopTenantOnHost(ctx, &routeTenant, targetPort)
				return rollback("route", err)
			}
		}
		if err := a.store.setLocation(t.ID, targetID, baseURL, sourceDir); err != nil {
			if t.Domain != "" {
				_ = a.registerRouteForTenantHost(ctx, t, t.Domain, t.Domain, "tool:tenant_migrate_rollback")
			}
			return rollback("persist", err)
		}
		_ = a.store.setStatus(t.ID, newStatus, "user")
		_ = a.store.recordEvent(t.ID, "migrated", "user", map[string]any{
			"from_instance_id": t.InstanceID,
			"to_instance_id":   targetID,
			"base_url":         baseURL,
			"port":             targetPort,
			"same_host":        true,
		})
		out, _, _ := a.store.get(id)
		return map[string]any{"tenant": a.publicTenantView(out), "migrated": true, "instance_id": targetID, "base_url": baseURL}, nil
	}

	if err := a.transferTenantData(ctx, sourceHost, targetHost, sourceDir, targetDir, t.Slug, false); err != nil {
		return rollback("transfer", err)
	}
	targetStarted := false
	baseURL, newStatus, err := a.startTenantOnHost(ctx, targetHost, t.ID, t.Slug, targetDir, version, targetPort, prevStatus)
	if err != nil {
		_ = a.removeTenantData(ctx, targetHost, t.Slug, targetDir)
		return rollback("target start", err)
	}
	targetStarted = true
	if t.Domain != "" {
		routeTenant := *t
		routeTenant.InstanceID = targetID
		routeTenant.BaseURL = baseURL
		routeTenant.ConfigDir = targetDir
		if err := a.registerRouteForTenantHost(ctx, &routeTenant, t.Domain, t.Domain, "tool:tenant_migrate"); err != nil {
			_ = a.stopTenantOnHost(ctx, &routeTenant, targetPort)
			_ = a.removeTenantData(ctx, targetHost, t.Slug, targetDir)
			return rollback("route", err)
		}
	}

	cleanupTarget := func() {
		if targetStarted {
			if targetHost.IsLocal() {
				_ = a.stopTenantBy(t.Slug, targetDir, targetPort, 10*time.Second)
			} else {
				_ = stopHostedTenant(ctx, targetHost.InstanceID, t.Slug, targetPort, 10*time.Second)
			}
		}
		_ = a.removeTenantData(ctx, targetHost, t.Slug, targetDir)
	}
	var retained *RetainedSource
	if retainSource {
		retained = &RetainedSource{
			TenantID:         t.ID,
			SourceInstanceID: sourceHost.InstanceID,
			SourceConfigDir:  sourceDir,
			SourceSlug:       t.Slug,
		}
		if err := a.store.createRetainedSource(retained); err != nil {
			cleanupTarget()
			if t.Domain != "" {
				_ = a.registerRouteForTenantHost(ctx, t, t.Domain, t.Domain, "tool:tenant_migrate_rollback")
			}
			return rollback("record retained source", err)
		}
	}

	if err := a.store.setLocation(t.ID, targetID, baseURL, targetDir); err != nil {
		if retained != nil {
			_ = a.store.deleteRetainedSource(t.ID)
		}
		cleanupTarget()
		if t.Domain != "" {
			_ = a.registerRouteForTenantHost(ctx, t, t.Domain, t.Domain, "tool:tenant_migrate_rollback")
		}
		return rollback("persist", err)
	}
	_ = a.store.setStatus(t.ID, newStatus, "user")
	if !retainSource && (sourceHost.InstanceID != targetHost.InstanceID || sourceDir != targetDir) {
		if err := a.removeTenantData(ctx, sourceHost, t.Slug, sourceDir); err != nil {
			return nil, fmt.Errorf("migration committed but source cleanup failed: %w", err)
		}
	}
	_ = a.store.recordEvent(t.ID, "migrated", "user", map[string]any{
		"from_instance_id": t.InstanceID,
		"to_instance_id":   targetID,
		"base_url":         baseURL,
		"port":             targetPort,
		"source_retained":  retainSource,
	})
	out, _, _ := a.store.get(id)
	return map[string]any{
		"tenant":          a.publicTenantView(out),
		"migrated":        true,
		"instance_id":     targetID,
		"base_url":        baseURL,
		"source_retained": retainSource,
		"retained_source": retained,
	}, nil
}

// toolMigrateFinalize permanently removes a source retained by a successful
// migration. Without confirm=true it is a read-only preview.
func (a *App) toolMigrateFinalize(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := strings.TrimSpace(getStr(args, "tenant_id"))
	if id == "" {
		return nil, errors.New("tenant_id required")
	}
	t, _, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	done, err := a.beginTenantOperation(t.ID, "migrate_finalize")
	if err != nil {
		return nil, err
	}
	defer done()

	retained, err := a.store.getRetainedSource(id)
	if err != nil {
		return nil, err
	}
	if retained == nil {
		return map[string]any{"finalized": false, "note": "tenant has no retained migration source"}, nil
	}
	if retained.SourceInstanceID == t.InstanceID && retained.SourceConfigDir == t.ConfigDir {
		return nil, errors.New("refusing to finalize: retained source matches the tenant's current location")
	}
	if !boolArg(args, "confirm") {
		return map[string]any{
			"finalized":             false,
			"requires_confirmation": true,
			"retained_source":       retained,
		}, nil
	}

	sourceHost, err := a.resolveFleetHost(ctx, retained.SourceInstanceID)
	if err != nil {
		return nil, fmt.Errorf("resolve retained source host: %w", err)
	}
	if err := a.removeTenantData(ctx, sourceHost, retained.SourceSlug, retained.SourceConfigDir); err != nil {
		return nil, fmt.Errorf("remove retained source: %w", err)
	}
	if err := a.store.deleteRetainedSource(id); err != nil {
		return nil, fmt.Errorf("remove retained source record: %w", err)
	}
	_ = a.store.recordEvent(id, "migration_source_finalized", "tool:tenant_migrate_finalize", map[string]any{
		"source_instance_id": retained.SourceInstanceID,
		"source_config_dir":  retained.SourceConfigDir,
	})
	return map[string]any{"finalized": true, "tenant_id": id}, nil
}
