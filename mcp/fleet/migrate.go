package main

// Tenant migration — move a tenant's data dir + running process from
// one host to another. v0.8 ships the local→instance direction (parent
// host → a remote VPS managed by the Instances app), which is the path
// operators actually need: spin a tenant up locally, then relocate it
// onto dedicated hardware once it matters.
//
// The transfer is a cold migration:
//
//   1. stop the local apteva-server (quiesces SQLite — no WAL races)
//   2. tar the local data dir
//   3. base64-upload the tarball via instances.instance_upload_file
//   4. extract on the VPS under /var/lib/apteva-fleet/<slug>/
//   5. spawn the apteva-server there (FreshSetup=false — existing DB,
//      so the admin + api_key travel with the data; fleet's sealed
//      api_key stays valid)
//   6. health-probe; on success update the row + re-point the route;
//      on failure roll back by restarting the local process
//
// The tenant is briefly down for the duration (stop → transfer →
// boot). That's inherent to a cold move and is why it's an explicit,
// operator-confirmed action rather than something automatic.
//
// Size note: the tarball rides as one base64 MCP arg (held in memory on
// both fleet and the instances sidecar). Fine for typical tenant DBs
// (single-digit MB); guarded by migrateMaxTarballBytes so a runaway
// data dir fails loud rather than OOMing the sidecar.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Refuse to in-memory-base64 a data dir bigger than this. Operators
// with larger tenants should use a manual rsync + tenant_connect, or
// wait for a streaming transfer path (future).
const migrateMaxTarballBytes = 256 << 20 // 256 MB

// toolMigrate moves a LOCAL tenant onto a remote instance.
//
// Args: tenant_id (required), instance_id (required, >0), port?
// (hosted port; default pickHostedPort).
func (a *App) toolMigrate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := strings.TrimSpace(getStr(args, "tenant_id"))
	if id == "" {
		return nil, errors.New("tenant_id required")
	}
	instanceID := int64Arg(args, "instance_id")
	if instanceID <= 0 {
		return nil, errors.New("instance_id required and must be > 0 (local→local is a no-op; instance→local isn't supported yet)")
	}
	t, _, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	if t.IsHosted() {
		return nil, fmt.Errorf("tenant is already hosted on instance %d — instance→instance and instance→local moves aren't supported yet", t.InstanceID)
	}
	if t.Kind != KindLocal {
		return nil, fmt.Errorf("only local-kind tenants can be migrated (this one is %q)", t.Kind)
	}

	info, err := a.getInstanceInfo(ctx, instanceID)
	if err != nil {
		return nil, err
	}

	localDir := t.ConfigDir
	if localDir == "" {
		d, derr := slugDataDir(t.Slug)
		if derr != nil {
			return nil, derr
		}
		localDir = d
	}
	if _, statErr := os.Stat(localDir); statErr != nil {
		return nil, fmt.Errorf("local data dir %q not found: %w", localDir, statErr)
	}

	localPort, _ := portFromBaseURL(t.BaseURL)
	remotePort := a.pickHostedPort(instanceID, intArg(args, "port", 0))

	version := t.CurrentVersion
	if version == "" {
		version = t.TargetVersion
	}
	if version == "" {
		version = "latest"
	}

	a.store.recordEvent(t.ID, "migrate_start", "user", map[string]any{
		"to_instance_id": instanceID, "to_instance_ip": info.PublicIPv4,
		"from_port": localPort, "to_port": remotePort,
	})

	// 1) Stop local cleanly so SQLite is quiesced before we tar it.
	if localPort > 0 {
		_ = a.stopTenantBy(t.Slug, localPort, 10*time.Second)
	}
	a.procMu.Lock()
	delete(a.procs, t.Slug)
	a.procMu.Unlock()

	// rollback restarts the local process from the still-present data
	// dir and re-flags the row local/active. Called on any failure
	// after the stop, so a botched migration never leaves the tenant
	// dark.
	rollback := func(stage string, cause error) (any, error) {
		_ = a.store.recordEvent(t.ID, "migrate_failed", "user",
			map[string]any{"stage": stage, "error": cause.Error()})
		if localPort > 0 {
			if _, proc, rerr := a.spawnTenant(context.Background(), t.Slug, localDir,
				tenantAptevaBin(version), localPort, false); rerr == nil {
				a.procMu.Lock()
				a.procs[t.Slug] = proc
				a.procMu.Unlock()
				_ = a.store.setStatus(t.ID, StatusActive, "user")
				_ = a.store.recordEvent(t.ID, "migrate_rolled_back", "user",
					map[string]any{"restarted_local_port": localPort})
			} else {
				_ = a.store.setStatus(t.ID, StatusFailed, "user")
				_ = a.store.recordEvent(t.ID, "migrate_rollback_failed", "user",
					map[string]any{"error": rerr.Error()})
			}
		}
		return nil, fmt.Errorf("migrate %s: %w (local tenant restarted)", stage, cause)
	}

	// 2) tar the data dir → temp file on the parent.
	tarPath := filepath.Join(os.TempDir(),
		fmt.Sprintf("fleet-migrate-%s-%d.tgz", t.Slug, time.Now().Unix()))
	defer os.Remove(tarPath)
	// -C parent so the archive contains "<slug>/..." and extracts
	// straight into remoteFleetRoot.
	if out, terr := exec.Command("tar", "czf", tarPath,
		"-C", filepath.Dir(localDir), filepath.Base(localDir)).CombinedOutput(); terr != nil {
		return rollback("tar", fmt.Errorf("%w: %s", terr, strings.TrimSpace(string(out))))
	}
	raw, rerr := os.ReadFile(tarPath)
	if rerr != nil {
		return rollback("read tarball", rerr)
	}
	if len(raw) > migrateMaxTarballBytes {
		return rollback("size check", fmt.Errorf(
			"data dir tarball is %d bytes, over the %d limit for in-memory transfer — migrate this one by hand (rsync + tenant_connect)",
			len(raw), migrateMaxTarballBytes))
	}

	// 3) upload to the VPS.
	remoteTar := fmt.Sprintf("/tmp/fleet-migrate-%s.tgz", t.Slug)
	if err := callSiblingTool(ctx, "instances", "", "instance_upload_file", map[string]any{
		"id":          instanceID,
		"path":        remoteTar,
		"content_b64": base64.StdEncoding.EncodeToString(raw),
	}, nil); err != nil {
		return rollback("upload", err)
	}

	// 4) extract into the fleet root, then drop the tarball.
	if _, code, eerr := instanceRunCommand(ctx, instanceID, fmt.Sprintf(
		`mkdir -p %s && tar xzf %s -C %s && rm -f %s`,
		sh(remoteFleetRoot), sh(remoteTar), sh(remoteFleetRoot), sh(remoteTar),
	), 120); eerr != nil || code != 0 {
		return rollback("remote extract", fmt.Errorf("%v (exit %d)", eerr, code))
	}

	// 5) boot the apteva-server on the VPS against the moved data dir.
	_, baseURL, serr := a.spawnHostedTenant(ctx, hostedSpawnSpec{
		InstanceID: instanceID,
		InstanceIP: info.PublicIPv4,
		Slug:       t.Slug,
		Port:       remotePort,
		AptevaVer:  version,
		FreshSetup: false, // existing DB — no setup token to scrape
	})
	if serr != nil {
		return rollback("remote spawn", serr)
	}

	// 6) commit: flip the row to the new host, re-point the route, then
	//    delete the local copy. spawnHostedTenant already waited for
	//    /api/health, so the remote is live before we burn the original.
	remoteDir := remoteFleetRoot + "/" + t.Slug
	if err := a.store.setLocation(t.ID, instanceID, baseURL, remoteDir); err != nil {
		// The remote is up but we couldn't persist the move. Don't roll
		// back (that would double-run the tenant); surface loudly so the
		// operator can reconcile.
		return nil, fmt.Errorf("remote tenant is live at %s but persisting the move failed: %w — reconcile manually", baseURL, err)
	}
	_ = a.store.setStatus(t.ID, StatusActive, "user")

	// Re-register the route so public traffic targets the VPS instead of
	// the (now-gone) loopback port. registerRouteForTenant re-reads the
	// fresh row, so it picks up the new base_url/IsHosted target.
	if t.Domain != "" {
		a.registerRouteForTenant(ctx, t.ID, t.Domain)
	}

	// Local data dir is now redundant — remove it so the parent isn't
	// holding a stale copy that a future reconcile could try to boot.
	if err := os.RemoveAll(localDir); err != nil {
		ctx.Logger().Warn("fleet: migrate cleanup — remove local data dir", "tenant", t.ID, "dir", localDir, "err", err)
	}

	_ = a.store.recordEvent(t.ID, "migrated", "user", map[string]any{
		"to_instance_id": instanceID, "base_url": baseURL, "port": remotePort,
	})

	out, _, _ := a.store.get(id)
	return map[string]any{
		"tenant":      a.publicTenantView(out),
		"migrated":    true,
		"instance_id": instanceID,
		"base_url":    baseURL,
	}, nil
}
