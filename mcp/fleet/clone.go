package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) toolClone(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	sourceID := strings.TrimSpace(getStr(args, "source_tenant_id"))
	slug := strings.ToLower(strings.TrimSpace(getStr(args, "slug")))
	if sourceID == "" || slug == "" {
		return nil, errors.New("source_tenant_id and slug are required")
	}
	source, apiKeyEnc, err := a.store.get(sourceID)
	if err != nil {
		return nil, err
	}
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
	port, err := a.pickTenantPort(targetHost, intArg(args, "port", 0))
	if err != nil {
		return nil, err
	}
	start := true
	if v, ok := args["start"].(bool); ok {
		start = v
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
	defer func() {
		if cleanup {
			_ = a.store.hardDelete(clone.ID)
			a.removeTenantData(ctx, targetHost, slug, targetDir)
		}
	}()
	if err := a.transferTenantData(ctx, sourceHost, targetHost, sourceDir, targetDir, slug, true); err != nil {
		return nil, fmt.Errorf("transfer clone: %w", err)
	}
	_ = a.store.recordEvent(clone.ID, "cloned", "user", map[string]any{
		"source_tenant_id": source.ID,
		"source_slug":      source.Slug,
		"source_instance":  source.InstanceID,
		"instance_id":      targetID,
		"port":             port,
		"started":          start,
	})
	if !start {
		cleanup = false
		ctx.Logger().Info("fleet: tenant cloned without start", "source", source.ID, "clone", clone.ID, "slug", slug, "instance_id", targetID)
		return map[string]any{
			"tenant_id":        clone.ID,
			"source_tenant_id": source.ID,
			"slug":             slug,
			"base_url":         a.publicBaseURL(clone.BaseURL),
			"status":           StatusStopped,
			"started":          false,
			"instance_id":      targetID,
			"domains_copied":   false,
		}, nil
	}
	baseURL, newStatus, err := a.startTenantOnHost(ctx, targetHost, clone.ID, slug, targetDir, tenantVersion(source), port, source.Status)
	if err != nil {
		_ = a.store.setStatus(clone.ID, StatusFailed, "user")
		_ = a.store.recordEvent(clone.ID, "clone_start_failed", "user", map[string]any{"error": err.Error()})
		return nil, fmt.Errorf("start cloned tenant: %w", err)
	}
	if err := a.store.setLocation(clone.ID, targetID, baseURL, targetDir); err != nil {
		return nil, err
	}
	_ = a.store.setStatus(clone.ID, newStatus, "user")
	_ = a.store.recordEvent(clone.ID, "started", "user", map[string]any{"source_tenant_id": source.ID, "port": port})
	cleanup = false
	ctx.Logger().Info("fleet: tenant cloned", "source", source.ID, "clone", clone.ID, "slug", slug, "instance_id", targetID, "port", port)
	return map[string]any{
		"tenant_id":        clone.ID,
		"source_tenant_id": source.ID,
		"slug":             slug,
		"base_url":         a.publicBaseURL(baseURL),
		"status":           newStatus,
		"started":          true,
		"instance_id":      targetID,
		"domains_copied":   false,
	}, nil
}

func baseURLForHost(h fleetHost, port int) string {
	if h.IsLocal() {
		return fmt.Sprintf("http://localhost:%d", port)
	}
	return fmt.Sprintf("http://%s:%d", h.Info.PublicIPv4, port)
}
