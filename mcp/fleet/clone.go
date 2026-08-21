package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) toolClone(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	sourceID := strings.TrimSpace(getStr(args, "source_tenant_id"))
	if sourceID == "" {
		return nil, errors.New("source_tenant_id is required")
	}
	slug, err := validatedTenantSlug(getStr(args, "slug"))
	if err != nil {
		return nil, err
	}
	source, apiKeyEnc, err := a.store.get(sourceID)
	if err != nil {
		return nil, err
	}
	done, err := a.beginTenantOperation(source.ID, "clone snapshot")
	if err != nil {
		return nil, err
	}
	defer done()
	if source.Kind != KindLocal {
		return nil, fmt.Errorf("tenant %s is kind=%s; clone is only supported for Fleet-managed tenants", sourceID, source.Kind)
	}
	if _, _, err := a.store.getBySlug(slug); err == nil {
		return nil, fmt.Errorf("slug %q already in use", slug)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	sourceHost, err := a.resolveFleetHost(ctx, source.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("source host: %w", err)
	}
	targetID := source.InstanceID
	if _, ok := args["instance_id"]; ok {
		targetID = int64Arg(args, "instance_id")
	}
	targetHost, err := a.resolveFleetHost(ctx, targetID)
	if err != nil {
		return nil, fmt.Errorf("target host: %w", err)
	}
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
	sourceDir := source.ConfigDir
	if sourceDir == "" {
		sourceDir, err = tenantDataDirForHost(source.Slug, sourceHost)
		if err != nil {
			return nil, err
		}
	}
	targetDir, err := tenantDataDirForHost(slug, targetHost)
	if err != nil {
		return nil, err
	}
	port, err := a.pickTenantPort(ctx, targetHost, intArg(args, "port", 0))
	if err != nil {
		return nil, err
	}
	start := true
	if v, ok := args["start"].(bool); ok {
		start = v
	}
	cloneVersion := tenantVersion(source)
	if start && !supportsCloneRuntimeRecovery(cloneVersion) {
		return nil, fmt.Errorf("source tenant runs Apteva %q; clone rehearsal requires %s or newer — update the tenant before cloning", cloneVersion, cloneQuarantineMinAptevaVersion)
	}
	status := StatusStopped
	if start {
		status = StatusStarting
	}
	setupTokenEnc, err := a.store.getSetupToken(source.ID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	owner := strings.TrimSpace(getStr(args, "owner_email"))
	if owner == "" {
		owner = source.OwnerEmail
	}
	if sourceHost.IsLocal() {
		if _, err := os.Stat(sourceDir); err != nil {
			return nil, fmt.Errorf("source tenant data dir: %w", err)
		}
	}
	var requiredApps []tenantAppRuntime
	if start {
		requiredApps, err = a.tenantAppRuntimes(ctx, sourceHost, sourceDir)
		if err != nil {
			return nil, fmt.Errorf("inventory source app runtimes: %w", err)
		}
	}
	clone := &Tenant{
		Slug:           slug,
		Kind:           KindLocal,
		BaseURL:        baseURLForHost(targetHost, port),
		ConfigDir:      targetDir,
		OwnerEmail:     owner,
		OwnerUserID:    source.OwnerUserID,
		CurrentVersion: source.CurrentVersion,
		TargetVersion:  source.TargetVersion,
		Status:         status,
		InstanceID:     targetID,
	}
	if err := a.store.insert(clone, apiKeyEnc, setupTokenEnc); err != nil {
		return nil, err
	}
	cleanup := true
	targetStarted := false
	defer func() {
		if cleanup {
			if targetStarted {
				_ = a.stopTenantOnHost(ctx, clone, port)
			}
			_ = a.store.hardDelete(clone.ID)
			_ = a.removeTenantData(ctx, targetHost, slug, targetDir)
		}
	}()
	if err := a.transferTenantData(ctx, sourceHost, targetHost, sourceDir, targetDir, slug, true); err != nil {
		return nil, fmt.Errorf("transfer clone: %w", err)
	}
	a2aIdentityReset, err := a.resetClonedA2AState(ctx, targetHost, targetDir)
	if err != nil {
		return nil, fmt.Errorf("reset cloned A2A identity: %w", err)
	}
	_ = a.store.recordEvent(clone.ID, "cloned", "user", map[string]any{
		"source_tenant_id":   source.ID,
		"source_slug":        source.Slug,
		"source_instance":    source.InstanceID,
		"instance_id":        targetID,
		"port":               port,
		"started":            start,
		"a2a_identity_reset": a2aIdentityReset,
	})
	if !start {
		cleanup = false
		ctx.Logger().Info("fleet: tenant cloned without start", "source", source.ID, "clone", clone.ID, "slug", slug, "instance_id", targetID)
		return map[string]any{
			"tenant_id":          clone.ID,
			"source_tenant_id":   source.ID,
			"slug":               slug,
			"base_url":           a.publicBaseURL(clone.BaseURL),
			"status":             StatusStopped,
			"started":            false,
			"instance_id":        targetID,
			"domains_copied":     false,
			"a2a_identity_reset": a2aIdentityReset,
		}, nil
	}
	baseURL, _, err := a.startTenantOnHostMode(ctx, targetHost, clone, targetDir, cloneVersion, port, source.Status, true)
	if err != nil {
		_ = a.store.setStatus(clone.ID, StatusFailed, "user")
		_ = a.store.recordEvent(clone.ID, "clone_start_failed", "user", map[string]any{"error": err.Error()})
		return nil, fmt.Errorf("start cloned tenant: %w", err)
	}
	targetStarted = true
	if err := a.waitForQuarantinedRuntimes(ctx, targetHost, targetDir, requiredApps, 10*time.Minute); err != nil {
		_ = a.store.setStatus(clone.ID, StatusFailed, "user")
		_ = a.store.recordEvent(clone.ID, "clone_validation_failed", "user", map[string]any{"error": err.Error()})
		return nil, fmt.Errorf("validate cloned app runtimes: %w", err)
	}
	if err := a.stopTenantOnHost(ctx, clone, port); err != nil {
		return nil, fmt.Errorf("stop validated rehearsal clone: %w", err)
	}
	targetStarted = false
	if err := a.store.setLocation(clone.ID, targetID, baseURL, targetDir); err != nil {
		return nil, err
	}
	_ = a.store.setStatus(clone.ID, StatusStopped, "user")
	_ = a.store.recordEvent(clone.ID, "clone_rehearsal_validated", "user", map[string]any{"source_tenant_id": source.ID, "port": port})
	cleanup = false
	ctx.Logger().Info("fleet: tenant cloned", "source", source.ID, "clone", clone.ID, "slug", slug, "instance_id", targetID, "port", port)
	return map[string]any{
		"tenant_id":           clone.ID,
		"source_tenant_id":    source.ID,
		"slug":                slug,
		"base_url":            a.publicBaseURL(baseURL),
		"status":              StatusStopped,
		"started":             false,
		"rehearsal_validated": true,
		"instance_id":         targetID,
		"domains_copied":      false,
		"a2a_identity_reset":  a2aIdentityReset,
	}, nil
}

func baseURLForHost(h fleetHost, port int) string {
	if h.IsLocal() {
		return fmt.Sprintf("http://localhost:%d", port)
	}
	return fmt.Sprintf("http://%s:%d", h.Info.PublicIPv4, port)
}
