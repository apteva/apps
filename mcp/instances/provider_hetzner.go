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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// hetznerProvision does a best-effort end-to-end provisioning via the
// integration. Steps:
//   1. Generate a per-instance SSH keypair.
//   2. Persist the row at status='provisioning' (so the panel shows
//      progress immediately).
//   3. Call hetzner.server_create with a cloud-init userdata that
//      seeds authorized_keys with our new public key.
//   4. Parse provider_id + public IP from the response, persist.
//   5. Run the SSH readiness probe in the background; flip to 'ready'
//      when the box accepts our key.
//
// Returns the freshly-created instance row (status='provisioning'
// initially; caller can poll instance_get for the transition).
func hetznerProvision(ctx *sdk.AppCtx, in CreateInstanceInput) (*Instance, error) {
	bound := ctx.IntegrationFor("provider")
	if bound == nil || bound.ConnectionID == 0 {
		return nil, errors.New("no VPS provider bound — install the Hetzner integration and bind it to the 'provider' role on this install")
	}
	if bound.AppSlug != "" && bound.AppSlug != "hetzner" {
		return nil, fmt.Errorf("v0.1 only supports provider=hetzner; bound slug is %q", bound.AppSlug)
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
		in.Size = "cx22" // smallest current Hetzner shared-CPU tier
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
		_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{
			"status":        "error",
			"error_message": fmt.Sprintf("hetzner.server_create: %v", err),
		})
		return nil, fmt.Errorf("hetzner.server_create: %w", err)
	}
	if res == nil || !res.Success {
		msg := upstreamErrorString(res)
		_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{
			"status":        "error",
			"error_message": msg,
		})
		return nil, fmt.Errorf("hetzner.server_create returned status=%d: %s", upstreamStatus(res), msg)
	}
	provID, ipv4, ipv6 := parseHetznerCreateResponse(res.Data)
	if provID == "" {
		_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{
			"status":        "error",
			"error_message": "hetzner.server_create response missing server id; catalog shape may be out of sync with upstream API",
		})
		return nil, errors.New("hetzner.server_create response missing server id (catalog/upstream mismatch)")
	}
	_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{
		"provider_id":  provID,
		"public_ipv4":  ipv4,
		"public_ipv6":  ipv6,
	})

	// Background readiness probe — let the caller's instance_create
	// return immediately; a separate instance_wait_ready or polling
	// instance_get drives the transition from 'provisioning' to
	// 'ready'. Hetzner servers come up in 30-60s typically.
	kickReadinessProbe(inst.ID)

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
func kickReadinessProbe(id int64) {
	go func() {
		fresh, err := dbGetInstance(globalCtx.AppDB(), id)
		if err != nil {
			return
		}
		if err := probeSSHReady(fresh, 5*time.Minute); err != nil {
			_ = dbUpdateInstance(globalCtx.AppDB(), id, map[string]any{
				"status":        "error",
				"error_message": fmt.Sprintf("ssh probe: %v", err),
			})
			return
		}
		_ = dbUpdateInstance(globalCtx.AppDB(), id, map[string]any{
			"status":   "ready",
			"ready_at": nowUTC(),
		})
	}()
}

// reconcileHetznerProvisioning recovers rows left in 'provisioning' by
// a previous sidecar instance. Two states to handle, both caused by
// the previous sidecar dying mid-flight:
//
//  1. provider_id is empty → upstream server_create may have succeeded
//     but we never persisted the result. Query Hetzner for any server
//     with our `name`; if found, backfill provider_id + IPs and kick
//     the readiness probe. If not found, mark error so the operator
//     can clean up the row (and won't be billed for a VPS we never
//     attached to).
//
//  2. provider_id is set, status still 'provisioning' → the readiness
//     probe goroutine evaporated when the sidecar died. Just kick a
//     new one against the existing IP.
//
// Best-effort, errors logged but don't fail OnMount. If the Hetzner
// integration was unbound between previous boot and now, every
// recovery in (1) marks error with a clear message; the operator
// re-binds the integration and re-runs reconcile (manual destroy /
// retry) from the panel.
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

	bound := ctx.IntegrationFor("provider")
	hasIntegration := bound != nil && bound.ConnectionID != 0

	for _, inst := range rows {
		// Path 2: just kick the probe; nothing upstream to recover.
		if inst.ProviderID != "" {
			ctx.Logger().Info("instances: re-kick readiness probe", "id", inst.ID, "provider_id", inst.ProviderID)
			kickReadinessProbe(inst.ID)
			continue
		}
		// Path 1: missing provider_id — we may have leaked a VPS.
		if !hasIntegration {
			_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{
				"status":        "error",
				"error_message": "provisioning interrupted; Hetzner integration not bound — re-bind and retry, then check the Hetzner dashboard for an orphan server named " + inst.Name,
			})
			ctx.Logger().Warn("instances: stuck provisioning, no integration bound",
				"id", inst.ID, "name", inst.Name)
			continue
		}
		recovered, recErr := tryRecoverHetznerByName(ctx, bound, inst)
		if recErr != nil {
			_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{
				"status":        "error",
				"error_message": fmt.Sprintf("provisioning interrupted; reconcile lookup failed: %v — check the Hetzner dashboard for an orphan server named %q", recErr, inst.Name),
			})
			continue
		}
		if recovered == nil {
			// server_list succeeded but no match. Either the operator
			// already cleaned it up, or it never got created. Safe to
			// mark error — destroy will short-circuit (no provider_id).
			_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{
				"status":        "error",
				"error_message": "provisioning interrupted; no upstream server named " + inst.Name + " found — presumed not created or already destroyed",
			})
			ctx.Logger().Info("instances: stuck provisioning, no upstream match", "id", inst.ID, "name", inst.Name)
			continue
		}
		// Backfill and resume the readiness probe.
		_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{
			"provider_id": recovered.ID,
			"public_ipv4": recovered.IPv4,
			"public_ipv6": recovered.IPv6,
		})
		ctx.Logger().Info("instances: recovered orphan provisioning row",
			"id", inst.ID, "provider_id", recovered.ID, "ipv4", recovered.IPv4)
		kickReadinessProbe(inst.ID)
	}
}

type hetznerServerSummary struct {
	ID   string
	IPv4 string
	IPv6 string
}

// tryRecoverHetznerByName asks Hetzner for any server matching our
// instance name. If exactly one matches, returns it. Multiple matches
// (operator created another server with the same name out-of-band):
// return the lexicographically-first id — arbitrary but deterministic;
// the operator can re-destroy the wrong pick and re-reconcile.
// No matches: returns nil, nil (not an error).
func tryRecoverHetznerByName(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, inst *Instance) (*hetznerServerSummary, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "server_list", map[string]any{
		"name": inst.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("server_list: %w", err)
	}
	if res == nil || !res.Success {
		return nil, fmt.Errorf("server_list: %s", upstreamErrorString(res))
	}
	servers := parseHetznerListResponse(res.Data, inst.Name)
	if len(servers) == 0 {
		return nil, nil
	}
	// Pick the first match — stable, lets operator iterate if wrong.
	return &servers[0], nil
}

// parseHetznerListResponse extracts (id, ipv4, ipv6) tuples from a
// server_list response, filtered to those whose name == wantName.
// Tolerant of catalog envelope shapes — same defensive approach as
// parseHetznerCreateResponse.
func parseHetznerListResponse(data json.RawMessage, wantName string) []hetznerServerSummary {
	if len(data) == 0 {
		return nil
	}
	var v struct {
		Servers []struct {
			ID        any    `json:"id"`
			Name      string `json:"name"`
			PublicNet struct {
				IPv4 struct{ IP string `json:"ip"` } `json:"ipv4"`
				IPv6 struct{ IP string `json:"ip"` } `json:"ipv6"`
			} `json:"public_net"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil
	}
	out := make([]hetznerServerSummary, 0, len(v.Servers))
	for _, s := range v.Servers {
		if s.Name != wantName {
			continue
		}
		if s.ID == nil {
			continue
		}
		out = append(out, hetznerServerSummary{
			ID:   fmt.Sprintf("%v", s.ID),
			IPv4: s.PublicNet.IPv4.IP,
			IPv6: s.PublicNet.IPv6.IP,
		})
	}
	return out
}

// hetznerDestroy terminates the upstream resource. Idempotent on
// already-destroyed instances (Hetzner returns 404 → we soft-pass).
func hetznerDestroy(ctx *sdk.AppCtx, inst *Instance) error {
	bound := ctx.IntegrationFor("provider")
	if bound == nil || bound.ConnectionID == 0 {
		return errors.New("no VPS provider bound")
	}
	if inst.ProviderID == "" {
		// Nothing to delete upstream — local row will be cleared by
		// the caller. Happens when provisioning errored before the
		// upstream id was recorded.
		return nil
	}
	args := map[string]any{"id": inst.ProviderID}
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

// buildCloudInit builds a #cloud-config userdata string that seeds
// the public key into root's authorized_keys. Minimal — no package
// installs, no service setup. Consumer apps (Live Link, Deploy)
// install their own software via instance_run_command after the
// box is up.
func buildCloudInit(pubKey string) string {
	return strings.Join([]string{
		"#cloud-config",
		"users:",
		"  - name: root",
		"    ssh_authorized_keys:",
		"      - " + pubKey,
		"ssh_pwauth: false",
		"disable_root: false",
	}, "\n") + "\n"
}

// parseHetznerCreateResponse pulls the server id + public IPs from a
// Hetzner server_create response. Hetzner's upstream returns an
// envelope like:
//   {"server": {"id": 12345, "public_net": {"ipv4": {"ip": "..."}, "ipv6": {"ip": "..."}}}, ...}
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
				IPv4 struct{ IP string `json:"ip"` } `json:"ipv4"`
				IPv6 struct{ IP string `json:"ip"` } `json:"ipv6"`
			} `json:"public_net"`
		} `json:"server"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return "", "", ""
	}
	if v.Server.ID != nil {
		id = fmt.Sprintf("%v", v.Server.ID)
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
		if err := json.Unmarshal(data, &flat); err == nil && flat.ID != nil {
			id = fmt.Sprintf("%v", flat.ID)
			ipv4 = flat.IP
			ipv6 = flat.IP6
		}
	}
	return id, ipv4, ipv6
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
