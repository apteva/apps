package main

// Hetzner provisioner. Goes through the bound `provider` integration
// (kind=integration, slug=hetzner) via PlatformAPI.ExecuteIntegrationTool —
// no direct HTTP to Hetzner from this app.
//
// v0.1.0 status: integration shape relies on the catalog's hetzner.json
// being correct for upstream Hetzner Cloud API. If the catalog drifts
// (tool name / parameter shape mismatch), provisioning surfaces a
// clear error and the caller can fall back to local-only. Catalog
// alignment is a separate concern from this app's release.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// hetznerProvision does a best-effort end-to-end provisioning via the
// integration. Steps:
//  1. Generate a per-instance SSH keypair.
//  2. Persist the row at status='provisioning' (so the panel shows
//     progress immediately).
//  3. Call hetzner.server_create with a cloud-init userdata that
//     seeds authorized_keys with our new public key.
//  4. Parse provider_id + public IP from the response, persist.
//  5. Run the SSH readiness probe in the background; flip to 'ready'
//     when the box accepts our key.
//
// Returns the freshly-created instance row (status='provisioning'
// initially; caller can poll instance_get for the transition).
func hetznerProvision(ctx *sdk.AppCtx, in CreateInstanceInput) (*Instance, error) {
	bound, err := storageBinding(ctx, "hetzner", in.ProviderConnectionID)
	if err != nil {
		return nil, err
	}

	privKey, pubKey, err := generateSSHKeypair()
	if err != nil {
		return nil, fmt.Errorf("generate ssh keypair: %w", err)
	}
	in.SSHPrivateKey = privKey
	in.SSHPublicKey = pubKey
	in.SSHUser = "root"
	if in.Image == "" {
		in.Image = "ubuntu-24.04"
	}
	if in.Size == "" {
		in.Size = "cpx22" // smallest current Hetzner shared-CPU tier in the default fsn1 region
	}
	if in.Region == "" {
		in.Region = "fsn1"
	}
	in.Provider = "hetzner"
	in.Status = "provisioning"

	// Persist the row first so the panel can show "provisioning…"
	// before we wait on the upstream API. The provider_id stays empty
	// until step 4 fills it in.
	inst, err := dbCreateInstance(ctx.AppDB(), in)
	if err != nil {
		return nil, err
	}
	defer trackInstanceCreation(ctx, inst.ID)()
	emitInstanceCreated(ctx, inst)
	emitInstanceStatus(ctx, inst)

	cloudInit := buildCloudInit(pubKey)

	// Hetzner's upstream API takes an array of ssh_keys (existing key
	// ids registered on the account). We're passing our public key
	// inline via cloud-init userdata instead, which works on any
	// Ubuntu image without needing the user to pre-register a key
	// in their Hetzner account.
	args := map[string]any{
		"name":        in.Name,
		"server_type": in.Size,
		"image":       in.Image,
		"location":    in.Region,
		"user_data":   cloudInit,
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "server_create", args)
	if err != nil {
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{
			"status":        "error",
			"error_message": fmt.Sprintf("hetzner.server_create: %v", err),
		})
		return nil, fmt.Errorf("hetzner.server_create: %w", err)
	}
	if res == nil || !res.Success {
		msg := upstreamErrorString(res)
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{
			"status":        "error",
			"error_message": msg,
		})
		return nil, fmt.Errorf("hetzner.server_create returned status=%d: %s", upstreamStatus(res), msg)
	}
	provID, ipv4, ipv6 := parseHetznerCreateResponse(res.Data)
	if provID == "" {
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{
			"status":        "error",
			"error_message": "hetzner.server_create response missing server id; catalog shape may be out of sync with upstream API",
		})
		return nil, errors.New("hetzner.server_create response missing server id (catalog/upstream mismatch)")
	}
	if err := dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{
		"provider_id": provID,
		"public_ipv4": ipv4,
		"public_ipv6": ipv6,
	}); err != nil {
		orphan := *inst
		orphan.ProviderID, orphan.PublicIPv4, orphan.PublicIPv6 = provID, ipv4, ipv6
		cleanupErr := hetznerDestroy(ctx, &orphan)
		ctx.Logger().Error("instances: failed to persist Hetzner identity", "id", inst.ID, "provider_id", provID, "err", err, "cleanup_err", cleanupErr)
		if cleanupErr != nil {
			return nil, fmt.Errorf("persist created Hetzner server %s: %w; automatic cleanup also failed: %v", provID, err, cleanupErr)
		}
		return nil, fmt.Errorf("persist created Hetzner server %s: %w; upstream server was cleaned up", provID, err)
	}

	// Background readiness probe — let the caller's instance_create
	// return immediately; a separate instance_wait_ready or polling
	// instance_get drives the transition from 'provisioning' to
	// 'ready'. Hetzner servers come up in 30-60s typically.
	kickReadinessProbe(ctx, inst.ID)

	// Return the row as it stands now (status='provisioning', ip set).
	return dbGetInstance(ctx.AppDB(), inst.ID)
}

// kickReadinessProbe runs probeSSHReady in a goroutine and flips the
// instance to 'ready' (or 'error') when the probe resolves. Extracted
// from hetznerProvision so reconcileHetznerProvisioning can restart
// probes for rows orphaned by a sidecar restart.
//
// Best-effort: every error path lives inside the goroutine, the
// caller (provision or reconcile) doesn't block on this.
func kickReadinessProbe(ctx *sdk.AppCtx, id int64) {
	if !finishInstanceCreation(ctx, id) {
		return
	}
	probe := probeSSHReadyFn
	startInstanceWorker(ctx, id, func(work context.Context) {
		fresh, err := dbGetInstance(ctx.AppDB(), id)
		if err != nil {
			return
		}
		if fresh.Status != "provisioning" {
			return
		}
		fresh.workContext = work
		if err := probe(fresh, 5*time.Minute); err != nil {
			_, _ = updateInstanceAndEmit(ctx, id, map[string]any{
				"status":        "error",
				"error_message": fmt.Sprintf("ssh probe: %v", err),
			})
			return
		}
		if work.Err() != nil {
			return
		}
		_, _, _ = transitionInstanceAndEmit(ctx, id, []string{"provisioning"}, "ready", map[string]any{"ready_at": nowUTC(), "error_message": ""})
	})
}

// reconcileHetznerProvisioning recovers rows left in 'provisioning' by
// a previous sidecar instance. Two states to handle, both caused by
// the previous sidecar dying mid-flight:
//
//  1. provider_id is empty → upstream server_create may have succeeded
//     but we never persisted the response. We refuse to infer the
//     upstream id by name; duplicate cloud names are possible and a
//     wrong backfill would make destroy target the wrong server. Mark
//     the row error and tell the operator to inspect Hetzner manually.
//
//  2. provider_id is set, status still 'provisioning' → the readiness
//     probe goroutine evaporated when the sidecar died. Just kick a
//     new one against the recorded provider_id/IP.
//
// Best-effort, errors logged but don't fail OnMount.
func reconcileHetznerProvisioning(ctx *sdk.AppCtx) {
	rows, err := dbListInstances(ctx.AppDB(), "hetzner", "provisioning")
	if err != nil {
		ctx.Logger().Warn("instances: reconcile list failed", "err", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	ctx.Logger().Info("instances: reconciling provisioning rows", "count", len(rows))

	for _, inst := range rows {
		// Path 2: just kick the probe; nothing upstream to recover.
		if inst.ProviderID != "" {
			ctx.Logger().Info("instances: re-kick readiness probe", "id", inst.ID, "provider_id", inst.ProviderID)
			kickReadinessProbe(ctx, inst.ID)
			continue
		}
		// Path 1: missing provider_id. Do not recover by name; this
		// keeps destroy strictly bound to provider ids captured from
		// server_create responses.
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{
			"status":        "error",
			"error_message": "provisioning interrupted before Hetzner server id was recorded — Instances will not infer a server by name; check the Hetzner dashboard for an orphan server named " + inst.Name,
		})
		ctx.Logger().Warn("instances: stuck provisioning without provider_id; refusing name-based recovery",
			"id", inst.ID, "name", inst.Name)
	}
}

// hetznerDestroy terminates the upstream resource. Idempotent on
// already-destroyed instances (Hetzner returns 404 → we soft-pass).
func hetznerDestroy(ctx *sdk.AppCtx, inst *Instance) error {
	bound, err := storageBinding(ctx, "hetzner", inst.ProviderConnectionID)
	if err != nil {
		return err
	}
	if inst.ProviderID == "" {
		// Nothing to delete upstream — local row will be cleared by
		// the caller. Happens when provisioning errored before the
		// upstream id was recorded.
		return nil
	}
	args := map[string]any{"id": normalizeHetznerID(inst.ProviderID)}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "server_delete", args)
	if err != nil {
		return fmt.Errorf("hetzner.server_delete: %w", err)
	}
	if res == nil || !res.Success {
		// 404 = already gone, treat as success.
		if upstreamStatus(res) == 404 {
			return nil
		}
		return fmt.Errorf("hetzner.server_delete returned: %s", upstreamErrorString(res))
	}
	return nil
}

type UpgradeInstanceInput struct {
	Size        string
	UpgradeDisk bool
	Wait        bool
}

type UpgradeInstanceResult struct {
	InstanceID  int64  `json:"instance_id"`
	Provider    string `json:"provider"`
	OldSize     string `json:"old_size"`
	NewSize     string `json:"new_size"`
	Status      string `json:"status"`
	UpgradeDisk bool   `json:"upgrade_disk"`
}

// hetznerUpgrade changes an existing server's type in-place. It does
// not reinstall or migrate workloads; the same upstream server id,
// IPs, disk, and SSH key remain in place. Hetzner requires the server
// to be powered off before change_type, so this is an outage path.
func hetznerUpgrade(ctx *sdk.AppCtx, inst *Instance, in UpgradeInstanceInput) (*UpgradeInstanceResult, error) {
	if inst == nil {
		return nil, ErrInstanceNotFound
	}
	if inst.IsLocal() {
		return nil, ErrLocalInstanceImmutable
	}
	if inst.Provider != "hetzner" {
		return nil, fmt.Errorf("provider %q does not support in-place upgrade", inst.Provider)
	}
	if inst.ProviderID == "" {
		return nil, errors.New("instance has no provider_id")
	}
	if inst.Status != "ready" {
		return nil, fmt.Errorf("instance must be ready to upgrade (status=%s)", inst.Status)
	}
	if strings.TrimSpace(in.Size) == "" {
		return nil, errors.New("size required")
	}
	in.Size = strings.TrimSpace(in.Size)
	if in.Size == inst.Size {
		return nil, fmt.Errorf("instance is already size %q", inst.Size)
	}
	targetType, err := validateHetznerUpgradeTarget(ctx, inst, in.Size)
	if err != nil {
		return nil, err
	}

	bound, err := storageBinding(ctx, "hetzner", inst.ProviderConnectionID)
	if err != nil {
		return nil, err
	}

	oldSize := inst.Size
	if _, ok, err := transitionInstanceAndEmit(ctx, inst.ID, []string{"ready"}, "upgrading", map[string]any{
		"pending_size":  in.Size,
		"error_message": "",
	}); err != nil {
		return nil, err
	} else if !ok {
		return nil, errors.New("instance lifecycle changed before upgrade could start")
	}
	globalSSHPool.evict(inst.ID)
	clearMetricsCache(inst.ID)

	fail := func(format string, args ...any) (*UpgradeInstanceResult, error) {
		msg := fmt.Sprintf(format, args...)
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{
			"status":        "error",
			"error_message": msg,
		})
		return nil, errors.New(msg)
	}

	providerID := normalizeHetznerID(inst.ProviderID)
	shutdownErr := hetznerRunAction(ctx, bound.ConnectionID, "server_shutdown", map[string]any{"id": providerID}, 90*time.Second, true)
	if shutdownErr == nil {
		shutdownErr = waitHetznerServerStatus(ctx, bound.ConnectionID, providerID, "off", 90*time.Second)
	}
	if shutdownErr != nil {
		if err := hetznerRunAction(ctx, bound.ConnectionID, "server_poweroff", map[string]any{"id": providerID}, 90*time.Second, true); err != nil {
			return fail("hetzner shutdown/poweroff before upgrade: %v", err)
		}
		if err := waitHetznerServerStatus(ctx, bound.ConnectionID, providerID, "off", 90*time.Second); err != nil {
			return fail("hetzner wait for offline before upgrade: %v", err)
		}
	}

	if err := hetznerRunAction(ctx, bound.ConnectionID, "server_change_type", map[string]any{
		"id":           providerID,
		"server_type":  in.Size,
		"upgrade_disk": in.UpgradeDisk,
	}, 5*time.Minute, true); err != nil {
		_ = hetznerRunAction(ctx, bound.ConnectionID, "server_poweron", map[string]any{"id": providerID}, 90*time.Second, false)
		return fail("hetzner change_type: %v", err)
	}

	if err := hetznerRunAction(ctx, bound.ConnectionID, "server_poweron", map[string]any{"id": providerID}, 90*time.Second, true); err != nil {
		return fail("hetzner poweron after upgrade: %v", err)
	}
	if in.Wait {
		fresh, err := dbGetInstance(ctx.AppDB(), inst.ID)
		if err != nil {
			return nil, err
		}
		if err := probeSSHReadyFn(fresh, 5*time.Minute); err != nil {
			return fail("ssh probe after upgrade: %v", err)
		}
	}

	after, err := updateInstanceAndEmit(ctx, inst.ID, map[string]any{
		"status":             "ready",
		"size":               in.Size,
		"monthly_cost_cents": priceEURToCents(targetType.MonthlyPriceEUR),
		"error_message":      "",
		"pending_size":       "",
		"ready_at":           nowUTC(),
	})
	if err != nil {
		return nil, err
	}
	clearMetricsCache(inst.ID)
	emitInstanceUpgraded(ctx, inst, after, in.UpgradeDisk)
	return &UpgradeInstanceResult{
		InstanceID:  inst.ID,
		Provider:    inst.Provider,
		OldSize:     oldSize,
		NewSize:     in.Size,
		Status:      after.Status,
		UpgradeDisk: in.UpgradeDisk,
	}, nil
}

func reconcileHetznerUpgrading(ctx *sdk.AppCtx) {
	rows, err := dbListInstances(ctx.AppDB(), "hetzner", "upgrading")
	if err != nil {
		ctx.Logger().Warn("instances: reconcile upgrading list failed", "err", err)
		return
	}
	for _, inst := range rows {
		if err := recoverHetznerUpgrade(ctx, inst); err != nil {
			_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{"status": "error", "error_message": "upgrade recovery: " + err.Error()})
			ctx.Logger().Error("instances: upgrade recovery failed", "id", inst.ID, "err", err)
		}
	}
}

func recoverHetznerUpgrade(ctx *sdk.AppCtx, inst *Instance) error {
	bound, err := storageBinding(ctx, "hetzner", inst.ProviderConnectionID)
	if err != nil {
		return err
	}
	providerID := normalizeHetznerID(inst.ProviderID)
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "server_get", map[string]any{"id": providerID})
	if err != nil || res == nil || !res.Success {
		return fmt.Errorf("server_get: %v %s", err, upstreamErrorString(res))
	}
	status, err := parseHetznerServerStatus(res.Data)
	if err != nil {
		return err
	}
	if status == "off" {
		if err := hetznerRunAction(ctx, bound.ConnectionID, "server_poweron", map[string]any{"id": providerID}, 90*time.Second, true); err != nil {
			return err
		}
	}
	fresh, err := dbGetInstance(ctx.AppDB(), inst.ID)
	if err != nil {
		return err
	}
	if err := probeSSHReadyFn(fresh, 5*time.Minute); err != nil {
		return err
	}
	actualSize := parseHetznerServerType(res.Data)
	fields := map[string]any{"status": "ready", "pending_size": "", "ready_at": nowUTC(), "error_message": ""}
	if actualSize != "" {
		fields["size"] = actualSize
	}
	_, err = updateInstanceAndEmit(ctx, inst.ID, fields)
	return err
}

func validateHetznerUpgradeTarget(ctx *sdk.AppCtx, inst *Instance, size string) (*ServerType, error) {
	types, err := hetznerListServerTypes(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range types {
		if t.Name != size {
			continue
		}
		if t.Deprecated {
			return nil, fmt.Errorf("server type %q is deprecated", size)
		}
		if inst.Region != "" && len(t.AvailableIn) > 0 && !containsString(t.AvailableIn, inst.Region) {
			return nil, fmt.Errorf("server type %q is not available in region %q", size, inst.Region)
		}
		return &t, nil
	}
	return nil, fmt.Errorf("server type %q not found in live Hetzner catalog", size)
}

func hetznerRunAction(ctx *sdk.AppCtx, connectionID int64, tool string, args map[string]any, timeout time.Duration, wait bool) error {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, tool, args)
	if err != nil {
		return fmt.Errorf("%s: %w", tool, err)
	}
	if res == nil || !res.Success {
		return fmt.Errorf("%s returned: %s", tool, upstreamErrorString(res))
	}
	if !wait {
		return nil
	}
	actionID := parseHetznerActionID(res.Data)
	if actionID == 0 {
		return nil
	}
	return waitHetznerAction(ctx, connectionID, actionID, timeout)
}

func waitHetznerAction(ctx *sdk.AppCtx, connectionID int64, actionID int64, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "action_get", map[string]any{"id": actionID})
		if err != nil {
			return fmt.Errorf("action_get %d: %w", actionID, err)
		}
		if res == nil || !res.Success {
			return fmt.Errorf("action_get %d returned: %s", actionID, upstreamErrorString(res))
		}
		status, actionErr := parseHetznerActionStatus(res.Data)
		switch status {
		case "success":
			return nil
		case "error":
			if actionErr != "" {
				return fmt.Errorf("action %d failed: %s", actionID, actionErr)
			}
			return fmt.Errorf("action %d failed", actionID)
		case "", "running":
			// keep polling
		default:
			if status != "running" {
				return fmt.Errorf("action %d unexpected status %q", actionID, status)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("action %d timed out after %s", actionID, timeout)
		}
		if err := sleepOperation(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

func waitHetznerServerStatus(ctx *sdk.AppCtx, connectionID int64, providerID string, want string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "server_get", map[string]any{"id": providerID})
		if err != nil {
			return fmt.Errorf("server_get %s: %w", providerID, err)
		}
		if res == nil || !res.Success {
			return fmt.Errorf("server_get %s returned: %s", providerID, upstreamErrorString(res))
		}
		status, err := parseHetznerServerStatus(res.Data)
		if err != nil {
			return err
		}
		if status == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("server %s status is %q, want %q", providerID, status, want)
		}
		if err := sleepOperation(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

func parseHetznerServerStatus(data json.RawMessage) (string, error) {
	var v struct {
		Server struct {
			Status string `json:"status"`
		} `json:"server"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return "", fmt.Errorf("decode server status: %w", err)
	}
	if v.Server.Status == "" {
		return "", errors.New("server_get response missing server.status")
	}
	return v.Server.Status, nil
}

func parseHetznerServerType(data json.RawMessage) string {
	var v struct {
		Server struct {
			ServerType struct {
				Name string `json:"name"`
			} `json:"server_type"`
		} `json:"server"`
	}
	if json.Unmarshal(data, &v) != nil {
		return ""
	}
	return v.Server.ServerType.Name
}

func parseHetznerActionID(data json.RawMessage) int64 {
	var v struct {
		Action struct {
			ID any `json:"id"`
		} `json:"action"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return 0
	}
	return anyToInt64(v.Action.ID)
}

func parseHetznerActionStatus(data json.RawMessage) (status string, actionErr string) {
	var v struct {
		Action struct {
			Status string `json:"status"`
			Error  any    `json:"error"`
		} `json:"action"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return "", err.Error()
	}
	if v.Action.Error == nil {
		return v.Action.Status, ""
	}
	if b, err := json.Marshal(v.Action.Error); err == nil {
		return v.Action.Status, string(b)
	}
	return v.Action.Status, fmt.Sprint(v.Action.Error)
}

func anyToInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	default:
		return 0
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func priceEURToCents(v float64) int {
	if v <= 0 {
		return 0
	}
	return int(math.Round(v * 100))
}

// buildCloudInit builds a #cloud-config userdata string that seeds
// the public key into root's authorized_keys. Minimal — no package
// installs, no service setup. Consumer apps (Live Link, Deploy)
// install their own software via instance_run_command after the
// box is up.
//
// Hetzner's Ubuntu images ship with the root password marked expired
// (chage -d 0 root), which is fine for password auth but PAM also
// enforces it on key-based non-interactive SSH: every command exits 1
// before running with "WARNING: Your password has expired. Password
// change required but no TTY available." So we explicitly tell cloud-
// init NOT to expire passwords, plus defensive `chage` commands in
// case the image already did so before cloud-init reads chpasswd. We
// must reset both the expiry date and the "last changed" date: changing
// only max/expiry leaves a `chage -d 0 root` account blocked by PAM.
// This was observed on both Hetzner and DigitalOcean Ubuntu images.
func buildCloudInit(pubKey string) string {
	return strings.Join([]string{
		"#cloud-config",
		"users:",
		"  - name: root",
		"    ssh_authorized_keys:",
		"      - " + pubKey,
		"ssh_pwauth: false",
		"disable_root: false",
		"growpart:",
		"  mode: auto",
		"  devices: ['/']",
		"  ignore_growroot_disabled: false",
		"resize_rootfs: true",
		"chpasswd:",
		"  expire: false",
		"runcmd:",
		"  - chage -d $(date +%Y-%m-%d) -M 99999 -E -1 root",
	}, "\n") + "\n"
}

// parseHetznerCreateResponse pulls the server id + public IPs from a
// Hetzner server_create response. Hetzner's upstream returns an
// envelope like:
//
//	{"server": {"id": 12345, "public_net": {"ipv4": {"ip": "..."}, "ipv6": {"ip": "..."}}}, ...}
//
// We're tolerant of catalog-wrapping variations — try a few common
// shapes and return what we find. Empty values fall through to the
// caller's "catalog mismatch" error path.
func parseHetznerCreateResponse(data json.RawMessage) (id, ipv4, ipv6 string) {
	if len(data) == 0 {
		return "", "", ""
	}
	var v struct {
		Server struct {
			ID        any `json:"id"`
			PublicNet struct {
				IPv4 struct {
					IP string `json:"ip"`
				} `json:"ipv4"`
				IPv6 struct {
					IP string `json:"ip"`
				} `json:"ipv6"`
			} `json:"public_net"`
		} `json:"server"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return "", "", ""
	}
	if v.Server.ID != nil {
		id = hetznerIDString(v.Server.ID)
	}
	ipv4 = v.Server.PublicNet.IPv4.IP
	ipv6 = v.Server.PublicNet.IPv6.IP
	if id == "" {
		// Older catalog wrappers might flatten this. Try a flat shape.
		var flat struct {
			ID  any    `json:"id"`
			IP  string `json:"ipv4"`
			IP6 string `json:"ipv6"`
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&flat); err == nil && flat.ID != nil {
			id = hetznerIDString(flat.ID)
			ipv4 = flat.IP
			ipv6 = flat.IP6
		}
	}
	return id, ipv4, ipv6
}

func hetznerIDString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return strconv.FormatInt(i, 10)
		}
		return normalizeHetznerID(x.String())
	case string:
		return normalizeHetznerID(x)
	case float64:
		if isIntegralHetznerIDFloat(x) {
			return strconv.FormatInt(int64(x), 10)
		}
	}
	return normalizeHetznerID(fmt.Sprintf("%v", v))
}

func normalizeHetznerID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.ContainsAny(s, ".eE") {
		if f, err := strconv.ParseFloat(s, 64); err == nil && isIntegralHetznerIDFloat(f) {
			return strconv.FormatInt(int64(f), 10)
		}
	}
	return s
}

func isIntegralHetznerIDFloat(f float64) bool {
	const maxInt64AsFloat = float64(int64(^uint64(0) >> 1))
	return !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f) && f >= 0 && f <= maxInt64AsFloat
}

// upstreamErrorString / upstreamStatus extract a useful error string
// from an ExecuteResult on the failure path. Mirror of the helpers
// in code's import_github.go and deploy's domain_link.go.
func upstreamErrorString(res *sdk.ExecuteResult) string {
	if res == nil || len(res.Data) == 0 {
		return "no body"
	}
	var m map[string]any
	if err := json.Unmarshal(res.Data, &m); err == nil && m != nil {
		if e, ok := m["error"].(string); ok && e != "" {
			return e
		}
		if msg, ok := m["message"].(string); ok && msg != "" {
			return msg
		}
	}
	s := string(res.Data)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func upstreamStatus(res *sdk.ExecuteResult) int {
	if res == nil {
		return 0
	}
	return res.Status
}
