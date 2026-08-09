package main

import (
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const defaultProviderSlug = "hetzner"

var compatibleProviderSlugs = []string{
	"hetzner",
	"digitalocean",
	"contabo",
	"vultr",
	"aws-ec2",
	"scaleway",
	"huawei-cloud",
	"linode",
	"ovhcloud",
	"runpod",
}

func normalizeProvider(p string) string {
	return strings.ToLower(strings.TrimSpace(p))
}

func isCompatibleProvider(p string) bool {
	for _, slug := range compatibleProviderSlugs {
		if p == slug {
			return true
		}
	}
	return false
}

func resolveInstanceProvider(ctx *sdk.AppCtx, explicit string) (string, error) {
	provider := normalizeProvider(explicit)
	bound := ctx.IntegrationFor("provider")

	if provider == "" && bound != nil {
		provider = normalizeProvider(bound.AppSlug)
	}
	if provider == "" {
		provider = defaultProviderSlug
	}
	if provider == "local" {
		return "", ErrLocalInstanceImmutable
	}
	if !isCompatibleProvider(provider) {
		return "", fmt.Errorf("provider %q is not a compatible Instances VPS provider; compatible providers: %s", provider, strings.Join(compatibleProviderSlugs, ", "))
	}
	if bound != nil {
		boundSlug := normalizeProvider(bound.AppSlug)
		if boundSlug != "" && boundSlug != provider {
			return "", fmt.Errorf("provider %q requested but this Instances install is bound to %q", provider, boundSlug)
		}
	}
	return provider, nil
}

func providerAdapterUnavailable(provider, operation string) error {
	return fmt.Errorf("provider %q does not implement the Instances %s operation", provider, operation)
}

func instanceCapabilities(inst *Instance) InstanceCapabilities {
	if inst == nil {
		return InstanceCapabilities{}
	}
	if inst.IsLocal() {
		return InstanceCapabilities{Run: true, Upload: true, Download: true, Metrics: true}
	}
	if !isCompatibleProvider(normalizeProvider(inst.Provider)) {
		return InstanceCapabilities{}
	}
	cap := InstanceCapabilities{Run: true, Upload: true, Download: true, Metrics: true, Tunnel: true}
	switch normalizeProvider(inst.Provider) {
	case "hetzner":
		cap.Destroy, cap.Upgrade = true, true
	case "digitalocean", "runpod":
		cap.Destroy = true
	default:
		provider := normalizeProvider(inst.Provider)
		// Contabo only schedules contract cancellation; it does not expose
		// immediate instance deletion. Do not present that as Destroy.
		cap.Destroy = isAPIProvider(provider) && provider != "contabo"
	}
	if cap.Destroy && isScalewayAppleInstance(inst) && !scalewayAppleCanDelete(inst, time.Now()) {
		cap.Destroy = false
	}
	return cap
}

func provisionInstance(ctx *sdk.AppCtx, in CreateInstanceInput) (*Instance, error) {
	provider, err := resolveInstanceProvider(ctx, in.Provider)
	if err != nil {
		return nil, err
	}
	in.Provider = provider
	switch provider {
	case "hetzner":
		return hetznerProvision(ctx, in)
	case "digitalocean":
		return digitalOceanProvision(ctx, in)
	case "runpod":
		return runPodProvision(ctx, in)
	default:
		if isAPIProvider(provider) {
			return apiProviderProvision(ctx, in)
		}
		return nil, providerAdapterUnavailable(provider, "provisioning")
	}
}

func destroyProviderInstance(ctx *sdk.AppCtx, inst *Instance) error {
	switch normalizeProvider(inst.Provider) {
	case "hetzner":
		return hetznerDestroy(ctx, inst)
	case "digitalocean":
		return digitalOceanDestroy(ctx, inst)
	case "runpod":
		return runPodDestroy(ctx, inst)
	default:
		if isAPIProvider(normalizeProvider(inst.Provider)) {
			return apiProviderDestroy(ctx, inst)
		}
		return providerAdapterUnavailable(inst.Provider, "destroy")
	}
}

func destroyManagedInstance(ctx *sdk.AppCtx, inst *Instance) error {
	if inst == nil {
		return ErrInstanceNotFound
	}
	if !instanceCapabilities(inst).Destroy {
		return providerAdapterUnavailable(inst.Provider, "destroy")
	}
	previous := inst.Status
	_, claimed, err := transitionInstanceAndEmit(ctx, inst.ID,
		[]string{"pending", "provisioning", "ready", "error"}, "destroying", nil)
	if err != nil {
		return err
	}
	if !claimed {
		return fmt.Errorf("instance lifecycle operation already in progress (status=%s)", previous)
	}
	claimedInst, err := dbGetInstance(ctx.AppDB(), inst.ID)
	if err != nil {
		return err
	}
	if err := destroyProviderInstance(ctx, claimedInst); err != nil {
		_, _, _ = transitionInstanceAndEmit(ctx, inst.ID, []string{"destroying"}, previous, map[string]any{"error_message": err.Error()})
		return err
	}
	if err := deleteInstanceAndEmit(ctx, claimedInst); err != nil {
		return err
	}
	globalTunnelRegistry.closeInstance(inst.ID)
	globalSSHPool.evict(inst.ID)
	clearMetricsCache(inst.ID)
	return nil
}

func reconcileDestroying(ctx *sdk.AppCtx) {
	rows, err := dbListInstances(ctx.AppDB(), "", "destroying")
	if err != nil {
		ctx.Logger().Warn("instances: reconcile destroying list failed", "err", err)
		return
	}
	for _, inst := range rows {
		if err := destroyProviderInstance(ctx, inst); err != nil {
			ctx.Logger().Error("instances: destroy recovery failed", "id", inst.ID, "err", err)
			continue
		}
		if err := deleteInstanceAndEmit(ctx, inst); err != nil {
			ctx.Logger().Error("instances: destroy recovery delete failed", "id", inst.ID, "err", err)
			continue
		}
		globalTunnelRegistry.closeInstance(inst.ID)
		globalSSHPool.evict(inst.ID)
		clearMetricsCache(inst.ID)
	}
}

func upgradeProviderInstance(ctx *sdk.AppCtx, inst *Instance, in UpgradeInstanceInput) (*UpgradeInstanceResult, error) {
	switch normalizeProvider(inst.Provider) {
	case "hetzner":
		return hetznerUpgrade(ctx, inst, in)
	default:
		return nil, providerAdapterUnavailable(inst.Provider, "in-place upgrade")
	}
}
