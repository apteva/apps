package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

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
		return nil, fmt.Errorf("tenant %s is kind=%s; clone is only supported for Fleet-managed local tenants", sourceID, source.Kind)
	}
	if source.IsHosted() {
		return nil, errors.New("hosted VPS tenant clone is not implemented yet; clone currently supports parent-host local tenants only")
	}
	if source.ConfigDir == "" {
		return nil, errors.New("source tenant has no config_dir")
	}
	if _, err := os.Stat(source.ConfigDir); err != nil {
		return nil, fmt.Errorf("source tenant data dir: %w", err)
	}
	if _, _, err := a.store.getBySlug(slug); err == nil {
		return nil, fmt.Errorf("slug %q already in use", slug)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	configDir, err := slugDataDir(slug)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(configDir); err == nil {
		return nil, fmt.Errorf("data dir already exists: %s", configDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	port := intArg(args, "port", 0)
	if port <= 0 {
		port, err = allocatePort()
		if err != nil {
			return nil, err
		}
	} else if portInUse(port) {
		return nil, fmt.Errorf("port %d is already in use", port)
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
	clone := &Tenant{
		Slug:           slug,
		Kind:           KindLocal,
		BaseURL:        fmt.Sprintf("http://localhost:%d", port),
		ConfigDir:      configDir,
		OwnerEmail:     owner,
		OwnerUserID:    source.OwnerUserID,
		CurrentVersion: source.CurrentVersion,
		TargetVersion:  source.TargetVersion,
		Status:         status,
	}
	if err := a.store.insert(clone, apiKeyEnc, setupTokenEnc); err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = a.store.hardDelete(clone.ID)
			_ = os.RemoveAll(configDir)
		}
	}()
	if err := copyDirReadOnly(source.ConfigDir, configDir); err != nil {
		return nil, fmt.Errorf("copy source data dir: %w", err)
	}
	_ = a.store.recordEvent(clone.ID, "cloned", "user", map[string]any{
		"source_tenant_id": source.ID,
		"source_slug":      source.Slug,
		"port":             port,
		"started":          start,
	})
	if !start {
		cleanup = false
		ctx.Logger().Info("fleet: tenant cloned without start", "source", source.ID, "clone", clone.ID, "slug", slug)
		return map[string]any{
			"tenant_id":        clone.ID,
			"source_tenant_id": source.ID,
			"slug":             slug,
			"base_url":         a.publicBaseURL(clone.BaseURL),
			"status":           StatusStopped,
			"started":          false,
			"domains_copied":   false,
		}, nil
	}
	spawnCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, proc, err := a.spawnTenant(spawnCtx, slug, configDir, tenantAptevaBin(clone.TargetVersion), port, false)
	if err != nil {
		_ = a.store.setStatus(clone.ID, StatusFailed, "user")
		_ = a.store.recordEvent(clone.ID, "clone_start_failed", "user", map[string]any{"error": err.Error()})
		return nil, fmt.Errorf("start cloned tenant: %w", err)
	}
	a.procMu.Lock()
	a.procs[slug] = proc
	a.procMu.Unlock()
	newStatus := StatusActive
	if source.Status == StatusSetupPending {
		newStatus = StatusSetupPending
	}
	_ = a.store.setStatus(clone.ID, newStatus, "user")
	_ = a.store.recordEvent(clone.ID, "started", "user", map[string]any{"source_tenant_id": source.ID, "port": port})
	cleanup = false
	ctx.Logger().Info("fleet: tenant cloned", "source", source.ID, "clone", clone.ID, "slug", slug, "port", port)
	return map[string]any{
		"tenant_id":        clone.ID,
		"source_tenant_id": source.ID,
		"slug":             slug,
		"base_url":         a.publicBaseURL(clone.BaseURL),
		"status":           newStatus,
		"started":          true,
		"domains_copied":   false,
	}, nil
}

func copyDirReadOnly(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if rel == "." {
			return os.MkdirAll(target, info.Mode())
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if isSQLiteSidecar(path) {
			return nil
		}
		if filepath.Ext(path) == ".db" {
			return cloneSQLiteDB(path, target)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
}

func isSQLiteSidecar(path string) bool {
	return strings.HasSuffix(path, ".db-wal") || strings.HasSuffix(path, ".db-shm")
}

func cloneSQLiteDB(src, dst string) error {
	db, err := sql.Open("sqlite", "file:"+src+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	quoted := "'" + strings.ReplaceAll(dst, "'", "''") + "'"
	_, err = db.Exec("VACUUM INTO " + quoted)
	return err
}
