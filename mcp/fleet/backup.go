package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type fleetTenantBackupManifest struct {
	FormatVersion int       `json:"format_version"`
	Provider      string    `json:"provider"`
	ScopeKind     string    `json:"scope_kind"`
	GeneratedAt   time.Time `json:"generated_at"`
	Tenant        *Tenant   `json:"tenant"`
}

func (a *App) toolTenantBackupPlan(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := getStr(args, "tenant_id")
	if id == "" {
		return nil, errors.New("tenant_id required")
	}
	t, _, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	covered := []string{"fleet tenant metadata", "local tenant data directory"}
	gaps := []string{}
	if t.Kind != KindLocal {
		gaps = append(gaps, "connected remote tenants are not filesystem-managed by Fleet")
	}
	if t.IsHosted() {
		gaps = append(gaps, "hosted VPS tenant snapshots are not implemented yet; remote /var/lib/apteva-fleet data must be backed up on that instance")
	}
	if t.ConfigDir == "" {
		gaps = append(gaps, "tenant has no config_dir")
	}
	return map[string]any{
		"tenant":       a.publicTenantView(t),
		"scope_kind":   "fleet_tenant",
		"scope_id":     t.ID,
		"source_app":   "fleet",
		"covered":      covered,
		"gaps":         gaps,
		"restorable":   t.Kind == KindLocal && !t.IsHosted() && t.ConfigDir != "",
		"backup_app":   map[string]any{"scope_kind": "fleet_tenant", "scope_id": t.ID, "source_app": "fleet"},
		"restore_mode": "replace local tenant data dir, then restart if it was running",
	}, nil
}

func (a *App) toolFleetTenantSnapshot(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := getStr(args, "tenant_id")
	if id == "" {
		id = getStr(args, "scope_id")
	}
	if id == "" {
		return nil, errors.New("tenant_id required")
	}
	t, _, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	done, err := a.beginTenantOperation(t.ID, "backup snapshot")
	if err != nil {
		return nil, err
	}
	defer done()
	if t.Kind != KindLocal {
		return nil, fmt.Errorf("tenant %s is kind=%s; only Fleet-managed local tenants can be snapshotted", id, t.Kind)
	}
	if t.IsHosted() {
		return nil, errors.New("hosted tenant backup is not implemented yet; back up the remote /var/lib/apteva-fleet tenant directory on the instance")
	}
	if t.ConfigDir == "" {
		return nil, errors.New("tenant has no config_dir")
	}
	if err := validateLocalTenantDir(t.Slug, t.ConfigDir); err != nil {
		return nil, err
	}
	if _, err := os.Stat(t.ConfigDir); err != nil {
		return nil, fmt.Errorf("tenant data dir: %w", err)
	}

	port, _ := portFromBaseURL(t.BaseURL)
	wasRunning := port > 0 && portInUse(port)
	if wasRunning {
		if err := a.stopTenantBy(t.Slug, t.ConfigDir, port, 15*time.Second); err != nil {
			return nil, fmt.Errorf("stop tenant for snapshot: %w", err)
		}
	}
	finish := func(result any, snapshotErr error) (any, error) {
		if !wasRunning {
			return result, snapshotErr
		}
		restartErr := a.restartTenantAfterBackup(ctx, t)
		if restartErr != nil {
			_ = a.store.setStatus(t.ID, StatusFailed, "backup")
			if snapshotErr != nil {
				return nil, fmt.Errorf("snapshot failed: %v; restart also failed: %w", snapshotErr, restartErr)
			}
			return nil, fmt.Errorf("snapshot created but tenant restart failed: %w", restartErr)
		}
		return result, snapshotErr
	}

	manifest := fleetTenantBackupManifest{
		FormatVersion: 1,
		Provider:      "fleet",
		ScopeKind:     "fleet_tenant",
		GeneratedAt:   time.Now().UTC(),
		Tenant:        t,
	}
	if boolArg(args, "supports_streaming") {
		stage, err := os.MkdirTemp(transferScratchRoot(), "backup-*")
		if err != nil {
			return finish(nil, err)
		}
		payload := filepath.Join(stage, "tenant")
		if err := copyDirReadOnly(t.ConfigDir, payload); err != nil {
			_ = os.RemoveAll(stage)
			return finish(nil, err)
		}
		archiveURL, err := a.createBackupDownloadURL(ctx, payload, stage, manifest, tenantTransferTTL)
		if err != nil {
			_ = os.RemoveAll(stage)
			return finish(nil, err)
		}
		mb, _ := json.Marshal(manifest)
		_ = a.store.recordEvent(t.ID, "backup_snapshot_created", "backup", map[string]any{"mode": "stream"})
		return finish(map[string]any{
			"archive_url": archiveURL,
			"manifest":    json.RawMessage(mb),
			"streaming":   true,
		}, nil)
	}
	var buf bytes.Buffer
	if err := writeFleetTenantArchive(&buf, t.ConfigDir, manifest); err != nil {
		return finish(nil, err)
	}
	mb, _ := json.Marshal(manifest)
	_ = a.store.recordEvent(t.ID, "backup_snapshot_created", "backup", map[string]any{"bytes": buf.Len()})
	return finish(map[string]any{
		"archive_b64": base64.StdEncoding.EncodeToString(buf.Bytes()),
		"manifest":    json.RawMessage(mb),
		"bytes":       buf.Len(),
	}, nil)
}

func (a *App) toolFleetTenantRestore(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := getStr(args, "tenant_id")
	if id == "" {
		id = getStr(args, "scope_id")
	}
	if id == "" {
		return nil, errors.New("tenant_id required")
	}
	t, _, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	if err := validateRestorableFleetTenant(t); err != nil {
		return nil, err
	}
	if boolArg(args, "prepare_stream") {
		uploadURL, err := a.createBackupRestoreURL(ctx, t.ID, tenantTransferTTL)
		if err != nil {
			return nil, err
		}
		return map[string]any{"upload_url": uploadURL, "streaming": true}, nil
	}
	archiveB64 := getStr(args, "archive_b64")
	if archiveB64 == "" {
		return nil, errors.New("archive_b64 required")
	}
	raw, err := base64.StdEncoding.DecodeString(archiveB64)
	if err != nil {
		return nil, fmt.Errorf("decode archive_b64: %w", err)
	}
	return a.restoreFleetTenantArchive(ctx, id, bytes.NewReader(raw))
}

func (a *App) restoreFleetTenantArchive(ctx *sdk.AppCtx, id string, archive io.Reader) (map[string]any, error) {
	t, _, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	if err := validateRestorableFleetTenant(t); err != nil {
		return nil, err
	}
	done, err := a.beginTenantOperation(t.ID, "backup restore")
	if err != nil {
		return nil, err
	}
	defer done()
	stage, err := os.MkdirTemp("", "fleet-tenant-restore-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	manifest, payloadDir, err := extractFleetTenantArchive(archive, stage)
	if err != nil {
		return nil, err
	}
	if manifest.ScopeKind != "fleet_tenant" || manifest.Provider != "fleet" {
		return nil, errors.New("archive is not a Fleet tenant backup")
	}
	if manifest.Tenant == nil || manifest.Tenant.ID != t.ID || manifest.Tenant.Slug != t.Slug {
		return nil, fmt.Errorf("backup belongs to a different tenant; expected id=%s slug=%s", t.ID, t.Slug)
	}

	port, _ := portFromBaseURL(t.BaseURL)
	wasRunning := port > 0 && portInUse(port)
	if wasRunning {
		if err := a.stopTenantBy(t.Slug, t.ConfigDir, port, 15*time.Second); err != nil {
			return nil, fmt.Errorf("stop tenant for restore: %w", err)
		}
	}
	restartAfterFailure := func(cause error) (map[string]any, error) {
		if wasRunning {
			if restartErr := a.restartTenantAfterBackup(ctx, t); restartErr != nil {
				_ = a.store.setStatus(t.ID, StatusFailed, "backup")
				return nil, fmt.Errorf("%v; restart after restore failure also failed: %w", cause, restartErr)
			}
		}
		return nil, cause
	}

	backupDir := t.ConfigDir + ".prerestore-" + time.Now().UTC().Format("20060102-150405")
	if _, err := os.Stat(t.ConfigDir); err == nil {
		if err := os.Rename(t.ConfigDir, backupDir); err != nil {
			return restartAfterFailure(fmt.Errorf("backup current data dir: %w", err))
		}
	}
	if err := copyDir(payloadDir, t.ConfigDir); err != nil {
		_ = os.RemoveAll(t.ConfigDir)
		_ = os.Rename(backupDir, t.ConfigDir)
		return restartAfterFailure(fmt.Errorf("restore data dir: %w", err))
	}
	_ = a.store.recordEvent(t.ID, "backup_restored", "backup", map[string]any{"previous_data_dir": backupDir})
	if wasRunning {
		if err := a.restartTenantAfterBackup(ctx, t); err != nil {
			_ = a.store.setStatus(t.ID, StatusFailed, "backup")
			return nil, fmt.Errorf("restart after restore: %w", err)
		}
	}
	return map[string]any{
		"tenant_id":         t.ID,
		"status":            t.Status,
		"previous_data_dir": backupDir,
		"restarted":         wasRunning,
		"manifest":          manifest,
	}, nil
}

func validateRestorableFleetTenant(t *Tenant) error {
	if t == nil {
		return errors.New("tenant required")
	}
	if t.Kind != KindLocal {
		return fmt.Errorf("tenant %s is kind=%s; only Fleet-managed local tenants can be restored", t.ID, t.Kind)
	}
	if t.IsHosted() {
		return errors.New("hosted tenant restore is not implemented yet")
	}
	if t.ConfigDir == "" {
		return errors.New("tenant has no config_dir")
	}
	return validateLocalTenantDir(t.Slug, t.ConfigDir)
}

func (a *App) restartTenantAfterBackup(ctx *sdk.AppCtx, t *Tenant) error {
	port, _ := portFromBaseURL(t.BaseURL)
	if port == 0 {
		return nil
	}
	spawnCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, proc, err := a.spawnTenant(spawnCtx, t.ID, t.Slug, t.ConfigDir, tenantAptevaBin(t.TargetVersion), port, false)
	if err != nil {
		return err
	}
	a.procMu.Lock()
	a.procs[t.Slug] = proc
	a.procMu.Unlock()
	_ = a.store.setStatus(t.ID, t.Status, "backup")
	if ctx != nil {
		ctx.Logger().Info("fleet: tenant restarted after backup operation", "tenant", t.ID, "port", port)
	}
	return nil
}

func writeFleetTenantArchive(w io.Writer, dataDir string, manifest fleetTenantBackupManifest) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	mb, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o644, Size: int64(len(mb)), ModTime: now}); err != nil {
		return err
	}
	if _, err := tw.Write(mb); err != nil {
		return err
	}
	if err := filepath.Walk(dataDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil || rel == "." {
			return err
		}
		name := filepath.ToSlash(filepath.Join("tenant", rel))
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name = name
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return err
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}

func extractFleetTenantArchive(r io.Reader, stage string) (fleetTenantBackupManifest, string, error) {
	var manifest fleetTenantBackupManifest
	gz, err := gzip.NewReader(r)
	if err != nil {
		return manifest, "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	payloadDir := filepath.Join(stage, "tenant")
	files := 0
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return manifest, "", err
		}
		files++
		if files > maxExtractedTenantFiles || h.Size < 0 || total+h.Size > maxExtractedTenantBytes {
			return manifest, "", errors.New("fleet tenant backup exceeds extraction limits")
		}
		total += h.Size
		clean := filepath.Clean(filepath.FromSlash(h.Name))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return manifest, "", fmt.Errorf("unsafe archive path %q", h.Name)
		}
		if clean == "manifest.json" {
			b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return manifest, "", err
			}
			if err := json.Unmarshal(b, &manifest); err != nil {
				return manifest, "", err
			}
			continue
		}
		if clean == "tenant" || strings.HasPrefix(clean, "tenant"+string(filepath.Separator)) || strings.HasPrefix(clean, "tenant/") {
			rel := strings.TrimPrefix(strings.TrimPrefix(clean, "tenant/"), "tenant"+string(filepath.Separator))
			dst := filepath.Join(payloadDir, rel)
			if dst != payloadDir && !strings.HasPrefix(dst, payloadDir+string(filepath.Separator)) {
				return manifest, "", fmt.Errorf("unsafe archive path %q", h.Name)
			}
			if h.Typeflag == tar.TypeDir {
				if err := os.MkdirAll(dst, h.FileInfo().Mode()); err != nil {
					return manifest, "", err
				}
				continue
			}
			if h.Typeflag == tar.TypeSymlink {
				link := filepath.Clean(filepath.FromSlash(h.Linkname))
				if filepath.IsAbs(link) {
					return manifest, "", fmt.Errorf("unsafe absolute symlink %q", h.Linkname)
				}
				resolved := filepath.Clean(filepath.Join(filepath.Dir(dst), link))
				if resolved != payloadDir && !strings.HasPrefix(resolved, payloadDir+string(filepath.Separator)) {
					return manifest, "", fmt.Errorf("unsafe escaping symlink %q", h.Linkname)
				}
				if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
					return manifest, "", err
				}
				if err := os.Symlink(h.Linkname, dst); err != nil {
					return manifest, "", err
				}
				continue
			}
			if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
				return manifest, "", fmt.Errorf("unsupported backup archive entry type %d", h.Typeflag)
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return manifest, "", err
			}
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, h.FileInfo().Mode())
			if err != nil {
				return manifest, "", err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return manifest, "", err
			}
			if err := f.Close(); err != nil {
				return manifest, "", err
			}
		}
	}
	if manifest.FormatVersion != 1 {
		return manifest, "", fmt.Errorf("unsupported fleet tenant backup format %d", manifest.FormatVersion)
	}
	if _, err := os.Stat(payloadDir); err != nil {
		return manifest, "", fmt.Errorf("archive missing tenant payload: %w", err)
	}
	return manifest, payloadDir, nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		inErr := in.Close()
		outErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if inErr != nil {
			return inErr
		}
		return outErr
	})
}
