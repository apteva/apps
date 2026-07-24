package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const migrateMaxTarballBytes = 256 << 20 // 256 MB

type fleetHost struct {
	InstanceID int64
	Info       *instanceInfo
}

func (h fleetHost) IsLocal() bool { return h.InstanceID == 0 }

func (a *App) resolveFleetHost(ctx *sdk.AppCtx, instanceID int64) (fleetHost, error) {
	if instanceID == 0 {
		return fleetHost{}, nil
	}
	info, err := a.getInstanceInfo(ctx, instanceID)
	if err != nil {
		return fleetHost{}, err
	}
	return fleetHost{InstanceID: instanceID, Info: info}, nil
}

func tenantDataDirForHost(slug string, h fleetHost) (string, error) {
	if h.IsLocal() {
		return slugDataDir(slug)
	}
	return remoteFleetRoot + "/" + slug, nil
}

func (a *App) pickTenantPort(ctx *sdk.AppCtx, h fleetHost, override int) (int, error) {
	if override > 0 {
		if err := validateTenantPort(override); err != nil {
			return 0, err
		}
		if h.IsLocal() && portInUse(override) {
			return 0, fmt.Errorf("port %d is already in use", override)
		}
		if !h.IsLocal() {
			return a.pickHostedPort(ctx, h.InstanceID, override)
		}
		return override, nil
	}
	if h.IsLocal() {
		return allocatePort()
	}
	return a.pickHostedPort(ctx, h.InstanceID, 0)
}

func tenantVersion(t *Tenant) string {
	if t.CurrentVersion != "" {
		return t.CurrentVersion
	}
	if t.TargetVersion != "" {
		return t.TargetVersion
	}
	return "latest"
}

func statusAfterRestart(prev string) string {
	if prev == StatusSetupPending {
		return StatusSetupPending
	}
	return StatusActive
}

func (a *App) stopTenantOnHost(ctx *sdk.AppCtx, t *Tenant, port int) error {
	if t.InstanceID > 0 {
		return stopHostedTenant(ctx, t.InstanceID, t.Slug, port, 10*time.Second)
	}
	if port > 0 {
		if err := a.stopTenantBy(t.Slug, t.ConfigDir, port, 10*time.Second); err != nil {
			return err
		}
	}
	a.procMu.Lock()
	delete(a.procs, t.Slug)
	a.procMu.Unlock()
	return nil
}

func (a *App) startTenantOnHost(ctx *sdk.AppCtx, h fleetHost, tenant *Tenant, dir, version string, port int, prevStatus string) (baseURL, newStatus string, err error) {
	return a.startTenantOnHostMode(ctx, h, tenant, dir, version, port, prevStatus, false)
}

func (a *App) startTenantOnHostMode(ctx *sdk.AppCtx, h fleetHost, tenant *Tenant, dir, version string, port int, prevStatus string, quarantine bool) (baseURL, newStatus string, err error) {
	if tenant == nil {
		return "", "", errors.New("tenant required")
	}
	newStatus = statusAfterRestart(prevStatus)
	if h.IsLocal() {
		spawnCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, proc, err := a.spawnTenantWithMode(spawnCtx, tenant.ID, tenant.Slug, dir, tenantAptevaBin(version), port, false, quarantine)
		if err != nil {
			return "", "", err
		}
		a.procMu.Lock()
		a.procs[tenant.Slug] = proc
		a.procMu.Unlock()
		return fmt.Sprintf("http://localhost:%d", port), newStatus, nil
	}
	spec := hostedSpawnSpecForTenant(tenant, h.Info.PublicIPv4, port)
	spec.InstanceID = h.InstanceID
	spec.AptevaVer = version
	spec.Quarantine = quarantine
	_, baseURL, err = a.spawnHostedTenant(ctx, spec)
	if err != nil {
		return "", "", err
	}
	return baseURL, newStatus, nil
}

func (a *App) removeTenantData(ctx *sdk.AppCtx, h fleetHost, slug, dir string) error {
	if h.IsLocal() {
		if err := validateLocalTenantDir(slug, dir); err != nil {
			return err
		}
		return os.RemoveAll(dir)
	}
	if err := validateHostedTenantDir(slug, dir); err != nil {
		return err
	}
	return destroyHostedTenant(ctx, h.InstanceID, slug)
}

func makeTenantArchiveLocal(srcDir string) ([]byte, error) {
	stage, err := os.MkdirTemp("", "fleet-transfer-src-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	snap := filepath.Join(stage, "tenant")
	if err := copyDirReadOnly(srcDir, snap); err != nil {
		return nil, err
	}
	return tarDirContents(snap)
}

func tarDirContents(dir string) ([]byte, error) {
	tarPath := filepath.Join(os.TempDir(), fmt.Sprintf("fleet-transfer-%d.tgz", time.Now().UnixNano()))
	defer os.Remove(tarPath)
	if out, err := exec.Command("tar", "czf", tarPath, "-C", dir, ".").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("tar: %w: %s", err, strings.TrimSpace(string(out)))
	}
	raw, err := os.ReadFile(tarPath)
	if err != nil {
		return nil, err
	}
	if len(raw) > migrateMaxTarballBytes {
		return nil, fmt.Errorf("tenant archive is %d bytes, over the %d limit for in-memory transfer", len(raw), migrateMaxTarballBytes)
	}
	return raw, nil
}

func extractTenantArchiveLocal(raw []byte, dstDir string) error {
	if _, err := os.Stat(dstDir); err == nil {
		return fmt.Errorf("target data dir already exists: %s", dstDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return err
	}
	tarPath := filepath.Join(os.TempDir(), fmt.Sprintf("fleet-transfer-in-%d.tgz", time.Now().UnixNano()))
	defer os.Remove(tarPath)
	if err := os.WriteFile(tarPath, raw, 0o600); err != nil {
		return err
	}
	if out, err := exec.Command("tar", "xzf", tarPath, "-C", dstDir).CombinedOutput(); err != nil {
		_ = os.RemoveAll(dstDir)
		return fmt.Errorf("extract: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func makeTenantArchiveRemoteCold(ctx *sdk.AppCtx, instanceID int64, srcDir, slug string) ([]byte, error) {
	remoteTar := fmt.Sprintf("/tmp/fleet-transfer-%s-%d.tgz", slug, time.Now().UnixNano())
	cmd := fmt.Sprintf(`test -d %s && tar czf %s -C %s .`,
		sh(srcDir), sh(remoteTar), sh(srcDir))
	if _, code, err := instanceRunCommand(ctx, instanceID, cmd, 120); err != nil || code != 0 {
		return nil, fmt.Errorf("remote tar: %w (exit %d)", err, code)
	}
	defer instanceRunCommand(ctx, instanceID, fmt.Sprintf(`rm -f %s`, sh(remoteTar)), 10)
	return downloadRemoteFile(ctx, instanceID, remoteTar)
}

func makeTenantArchiveRemoteSnapshot(ctx *sdk.AppCtx, instanceID int64, srcDir, slug string) ([]byte, error) {
	remoteTar := fmt.Sprintf("/tmp/fleet-clone-%s-%d.tgz", slug, time.Now().UnixNano())
	script := fmt.Sprintf(`
set -eu
command -v python3 >/dev/null 2>&1 || { echo "python3 required for non-disruptive remote clone snapshots" >&2; exit 127; }
SRC=%s
SNAP=$(mktemp -d /tmp/fleet-clone-snap-XXXXXX)
cleanup() { rm -rf "$SNAP"; }
trap cleanup EXIT
python3 - "$SRC" "$SNAP" <<'PY'
import os, shutil, sqlite3, stat, sys
src, dst = sys.argv[1], sys.argv[2]
for root, dirs, files in os.walk(src, followlinks=False):
    rel = os.path.relpath(root, src)
    outdir = dst if rel == "." else os.path.join(dst, rel)
    os.makedirs(outdir, exist_ok=True)
    try:
        shutil.copystat(root, outdir, follow_symlinks=False)
    except Exception:
        pass
    for name in files:
        if name.endswith(".db-wal") or name.endswith(".db-shm"):
            continue
        s = os.path.join(root, name)
        d = os.path.join(outdir, name)
        st = os.lstat(s)
        if stat.S_ISLNK(st.st_mode):
            os.symlink(os.readlink(s), d)
        elif stat.S_ISREG(st.st_mode) and name.endswith(".db"):
            srcdb = sqlite3.connect("file:" + s + "?mode=ro", uri=True, timeout=5)
            dstdb = sqlite3.connect(d)
            srcdb.backup(dstdb)
            dstdb.close()
            srcdb.close()
            os.chmod(d, st.st_mode & 0o777)
        elif stat.S_ISREG(st.st_mode):
            shutil.copy2(s, d)
PY
tar czf %s -C "$SNAP" .
`, sh(srcDir), sh(remoteTar))
	if out, code, err := instanceRunCommand(ctx, instanceID, script, 180); err != nil || code != 0 {
		return nil, fmt.Errorf("remote snapshot: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	defer instanceRunCommand(ctx, instanceID, fmt.Sprintf(`rm -f %s`, sh(remoteTar)), 10)
	return downloadRemoteFile(ctx, instanceID, remoteTar)
}

func downloadRemoteFile(ctx *sdk.AppCtx, instanceID int64, path string) ([]byte, error) {
	var out struct {
		ContentB64 string `json:"content_b64"`
		Bytes      int    `json:"bytes"`
	}
	if err := callSiblingTool(ctx, "instances", "", "instance_download_file",
		map[string]any{"id": instanceID, "path": path}, &out); err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(out.ContentB64)
	if err != nil {
		return nil, fmt.Errorf("decode downloaded archive: %w", err)
	}
	if len(raw) > migrateMaxTarballBytes {
		return nil, fmt.Errorf("tenant archive is %d bytes, over the %d limit for in-memory transfer", len(raw), migrateMaxTarballBytes)
	}
	return raw, nil
}

func extractTenantArchiveRemote(ctx *sdk.AppCtx, instanceID int64, raw []byte, dstDir, slug string) error {
	if len(raw) > migrateMaxTarballBytes {
		return fmt.Errorf("tenant archive is %d bytes, over the %d limit for in-memory transfer", len(raw), migrateMaxTarballBytes)
	}
	remoteTar := fmt.Sprintf("/tmp/fleet-transfer-in-%s-%d.tgz", slug, time.Now().UnixNano())
	if err := callSiblingTool(ctx, "instances", "", "instance_upload_file", map[string]any{
		"id":          instanceID,
		"path":        remoteTar,
		"content_b64": base64.StdEncoding.EncodeToString(raw),
	}, nil); err != nil {
		return err
	}
	cmd := fmt.Sprintf(`
set -eu
test ! -e %s
mkdir -p %s
tar xzf %s -C %s
rm -f %s
`, sh(dstDir), sh(dstDir), sh(remoteTar), sh(dstDir), sh(remoteTar))
	if out, code, err := instanceRunCommand(ctx, instanceID, cmd, 120); err != nil || code != 0 {
		_, _, _ = instanceRunCommand(ctx, instanceID, fmt.Sprintf(`rm -f %s`, sh(remoteTar)), 10)
		return fmt.Errorf("remote extract: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	return nil
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
		if outErr != nil {
			return outErr
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
