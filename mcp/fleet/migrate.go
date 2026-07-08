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
// row/routes, then delete the old data dir. On failure before commit,
// Fleet restarts the original source and leaves the row unchanged.
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
	if t.InstanceID == targetID && (portOverride == 0 || portOverride == sourcePort) {
		return map[string]any{"tenant": a.publicTenantView(t), "migrated": false, "note": "tenant is already on that host"}, nil
	}
	targetDir, err := tenantDataDirForHost(t.Slug, targetHost)
	if err != nil {
		return nil, err
	}
	targetPort, err := a.pickTenantPort(targetHost, portOverride)
	if err != nil {
		return nil, err
	}
	version := tenantVersion(t)
	prevStatus := t.Status

	_ = a.store.recordEvent(t.ID, "migrate_start", "user", map[string]any{
		"from_instance_id": t.InstanceID,
		"to_instance_id":   targetID,
		"from_port":        sourcePort,
		"to_port":          targetPort,
	})

	a.stopTenantOnHost(ctx, t, sourcePort)
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
		if err := a.store.setLocation(t.ID, targetID, baseURL, sourceDir); err != nil {
			return rollback("persist", err)
		}
		_ = a.store.setStatus(t.ID, newStatus, "user")
		if t.Domain != "" {
			a.registerRouteForTenant(ctx, t.ID, t.Domain)
		}
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
		a.removeTenantData(ctx, targetHost, t.Slug, targetDir)
		return rollback("target start", err)
	}
	targetStarted = true

	if err := a.store.setLocation(t.ID, targetID, baseURL, targetDir); err != nil {
		if targetStarted {
			if targetHost.IsLocal() {
				_ = a.stopTenantBy(t.Slug, targetDir, targetPort, 10*time.Second)
			} else {
				_ = stopHostedTenant(ctx, targetHost.InstanceID, targetPort, 10*time.Second)
			}
			a.removeTenantData(ctx, targetHost, t.Slug, targetDir)
		}
		return rollback("persist", err)
	}
	_ = a.store.setStatus(t.ID, newStatus, "user")
	if t.Domain != "" {
		a.registerRouteForTenant(ctx, t.ID, t.Domain)
	}
	if sourceHost.InstanceID != targetHost.InstanceID || sourceDir != targetDir {
		a.removeTenantData(ctx, sourceHost, t.Slug, sourceDir)
	}
	_ = a.store.recordEvent(t.ID, "migrated", "user", map[string]any{
		"from_instance_id": t.InstanceID,
		"to_instance_id":   targetID,
		"base_url":         baseURL,
		"port":             targetPort,
	})
	out, _, _ := a.store.get(id)
	return map[string]any{
		"tenant":      a.publicTenantView(out),
		"migrated":    true,
		"instance_id": targetID,
		"base_url":    baseURL,
	}, nil
}
