package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

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

type InstanceProviderBinding struct {
	Provider     string `json:"provider"`
	ConnectionID int64  `json:"connection_id"`
	Default      bool   `json:"default"`
}

func providerSlugForBinding(ctx *sdk.AppCtx, bound *sdk.BoundIntegration) string {
	if bound == nil {
		return ""
	}
	slug := normalizeProvider(bound.AppSlug)
	if slug == "" && ctx != nil && ctx.PlatformAPI() != nil && bound.ConnectionID > 0 {
		if conn, err := ctx.PlatformAPI().GetConnection(bound.ConnectionID); err == nil && conn != nil {
			slug = normalizeProvider(conn.AppSlug)
		}
	}
	return slug
}

func boundInstanceProviders(ctx *sdk.AppCtx) []InstanceProviderBinding {
	if ctx == nil {
		return nil
	}
	out := make([]InstanceProviderBinding, 0)
	for _, bound := range ctx.IntegrationsFor("provider") {
		if bound == nil || bound.ConnectionID <= 0 {
			continue
		}
		slug := providerSlugForBinding(ctx, bound)
		if !isCompatibleProvider(slug) {
			continue
		}
		out = append(out, InstanceProviderBinding{
			Provider: slug, ConnectionID: bound.ConnectionID, Default: bound.IsDefault,
		})
	}
	return out
}

func instanceProviderBinding(ctx *sdk.AppCtx, provider string) (*sdk.BoundIntegration, error) {
	provider = normalizeProvider(provider)
	if !isCompatibleProvider(provider) {
		return nil, fmt.Errorf("provider %q is not a compatible Instances VPS provider; compatible providers: %s", provider, strings.Join(compatibleProviderSlugs, ", "))
	}
	if ctx == nil {
		return nil, errors.New("Instances app context is unavailable")
	}
	if connection := catalogScope(ctx).ConnectionID; connection > 0 {
		return storageBinding(ctx, provider, connection)
	}

	var first *sdk.BoundIntegration
	for _, bound := range ctx.IntegrationsFor("provider") {
		if bound == nil || bound.ConnectionID <= 0 || providerSlugForBinding(ctx, bound) != provider {
			continue
		}
		if first == nil {
			first = bound
		}
		if bound.IsDefault {
			return bound, nil
		}
	}
	if first != nil {
		return first, nil
	}
	bindings := boundInstanceProviders(ctx)
	if len(bindings) == 0 {
		return nil, fmt.Errorf("provider %q requested but no VPS provider is bound to this Instances install", provider)
	}
	boundSlugs := make([]string, 0, len(bindings))
	seen := map[string]bool{}
	for _, binding := range bindings {
		if !seen[binding.Provider] {
			boundSlugs = append(boundSlugs, binding.Provider)
			seen[binding.Provider] = true
		}
	}
	if len(boundSlugs) == 1 {
		return nil, fmt.Errorf("provider %q requested but this Instances install is bound to %q", provider, boundSlugs[0])
	}
	return nil, fmt.Errorf("provider %q requested but is not bound to this Instances install; bound providers: %s", provider, strings.Join(boundSlugs, ", "))
}

func resolveInstanceProvider(ctx *sdk.AppCtx, explicit string) (string, error) {
	provider := normalizeProvider(explicit)
	if provider == "" {
		if ctx == nil {
			return "", errors.New("Instances app context is unavailable")
		}
		bound := ctx.IntegrationFor("provider")
		if bound == nil || bound.ConnectionID <= 0 {
			return "", errors.New("no VPS provider bound to this Instances install")
		}
		provider = providerSlugForBinding(ctx, bound)
	}
	if provider == "local" {
		return "", ErrLocalInstanceImmutable
	}
	if !isCompatibleProvider(provider) {
		return "", fmt.Errorf("provider %q is not a compatible Instances VPS provider; compatible providers: %s", provider, strings.Join(compatibleProviderSlugs, ", "))
	}
	if _, err := instanceProviderBinding(ctx, provider); err != nil {
		return "", err
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
	cap := InstanceCapabilities{Run: true, Upload: true, Download: true, Metrics: true, Tunnel: true}
	switch normalizeProvider(inst.Provider) {
	case "external":
		cap.Metrics = false // the current remote collector is Linux-/proc-specific
		cap.Destroy = true  // forget the inventory row; never destroys the host
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
	bound, err := storageBinding(ctx, provider, in.ProviderConnectionID)
	if err != nil {
		return nil, err
	}
	in.ProviderConnectionID = bound.ConnectionID
	ctx, releaseScope := scopedCatalog(ctx, catalogOptions{ConnectionID: bound.ConnectionID, Zone: in.Region})
	defer releaseScope()
	if err := validateStorageRequest(provider, in.Storage); err != nil {
		return nil, fmt.Errorf("invalid storage request: %w", err)
	}
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
	case "external":
		return nil
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

type DestroyOptions struct {
	Force             bool  `json:"force"`
	RetainVolumes     *bool `json:"retain_volumes,omitempty"`
	RetainFlexibleIPs bool  `json:"retain_flexible_ips"`
}

func destroyManagedInstance(ctx *sdk.AppCtx, inst *Instance) error {
	return destroyManagedInstanceWithOptions(ctx, inst, DestroyOptions{})
}

func destroyManagedInstanceWithOptions(ctx *sdk.AppCtx, inst *Instance, options DestroyOptions) error {
	if inst == nil {
		return ErrInstanceNotFound
	}
	if !instanceCapabilities(inst).Destroy {
		return providerAdapterUnavailable(inst.Provider, "destroy")
	}
	// Retrying an interrupted destroy resumes its saved intent. New options may
	// only make cleanup safer (retain) or explicitly permit an unreachable guest.
	if inst.Status == "destroying" {
		var saved DestroyOptions
		if err := json.Unmarshal([]byte(inst.DestroyOptionsJSON), &saved); err != nil {
			return err
		}
		saved.Force = saved.Force || options.Force
		saved.RetainFlexibleIPs = saved.RetainFlexibleIPs || options.RetainFlexibleIPs
		if options.RetainVolumes != nil {
			saved.RetainVolumes = options.RetainVolumes
		}
		options = saved
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		return err
	}
	_, claimed, err := transitionInstanceAndEmit(ctx, inst.ID,
		[]string{"pending", "provisioning", "ready", "error", "destroying"}, "destroying",
		map[string]any{"lifecycle_stage": "Delete", "cleanup_error": "", "destroy_options_json": string(encoded)})
	if err != nil {
		return err
	}
	if !claimed {
		return ErrOperationSuperseded
	}
	cancelInstanceWorker(ctx, inst.ID)
	fresh, err := dbGetInstance(ctx.AppDB(), inst.ID)
	if err != nil {
		return err
	}
	if fresh.CreatePending {
		if !instanceCreateActive(ctx, fresh.ID) {
			_ = dbUpdateInstance(ctx.AppDB(), fresh.ID, map[string]any{"cleanup_error": "Provider create outcome is unknown; reconcile the recorded account before deletion"})
			return fmt.Errorf("provider create outcome is unknown; inventory retained for reconciliation")
		}
		return nil
	} // create completion will compensate
	return resumeInstanceDestroy(ctx, fresh)
}

func resumeInstanceDestroy(ctx *sdk.AppCtx, inst *Instance) (err error) {
	unlock, err := lockResource(ctx.AppDB(), "instance", inst.ID)
	if err != nil {
		return err
	}
	defer unlock()
	inst, err = dbGetInstance(ctx.AppDB(), inst.ID)
	if err != nil {
		return err
	}
	if inst.Status != "destroying" || inst.CreatePending {
		return ErrOperationSuperseded
	}
	var options DestroyOptions
	if err = json.Unmarshal([]byte(inst.DestroyOptionsJSON), &options); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"cleanup_error": err.Error()})
		}
	}()
	if err = reconcileAWSBootStorage(ctx, inst); err != nil {
		return err
	}
	if options.RetainVolumes != nil {
		if err = applyDestroyBootPolicy(ctx, inst, *options.RetainVolumes); err != nil {
			return err
		}
		policy := "with_instance"
		if *options.RetainVolumes {
			policy = "retain"
		}
		if _, err = ctx.AppDB().Exec(`UPDATE instance_volumes SET delete_policy=? WHERE (instance_id=? OR id IN(SELECT resource_id FROM resource_operations WHERE resource_kind='volume' AND json_extract(input_json,'$.InstanceID')=?)) AND managed=1`, policy, inst.ID, inst.ID); err != nil {
			return err
		}
	}
	if err = prepareVolumesForInstanceDestroy(ctx, inst.ID, options.Force); err != nil {
		return err
	}
	if isScalewayElasticMetalInstance(inst) {
		err = scalewayElasticMetalDestroyWithOptions(ctx, inst, options.RetainFlexibleIPs)
	} else {
		err = destroyProviderInstance(ctx, inst)
	}
	if err != nil {
		return err
	}
	if err = finalizeAWSBootVolumes(ctx, inst); err != nil {
		return err
	}
	if err = deleteInstanceAndEmit(ctx, inst); err != nil {
		return err
	}
	globalTunnelRegistry.closeInstance(inst.ID)
	globalSSHPool.evict(inst.ID)
	clearMetricsCache(inst.ID)
	return nil
}

// Called only once the create response and all synchronous initialization have
// been recorded. A cancellation keeps the row until its upstream ID is known.
func finishInstanceCreation(ctx *sdk.AppCtx, id int64) bool {
	if err := dbUpdateInstance(ctx.AppDB(), id, map[string]any{"create_pending": false}); err != nil {
		return false
	}
	inst, err := dbGetInstance(ctx.AppDB(), id)
	if err != nil {
		return false
	}
	if inst.Status == "destroying" {
		if err := resumeInstanceDestroy(ctx, inst); err != nil {
			ctx.Logger().Warn("cancelled create cleanup pending", "id", id, "err", err)
		}
		return false
	}
	return inst.Status == "provisioning"
}

func reconcileDestroying(ctx *sdk.AppCtx) {
	rows, err := dbListInstances(ctx.AppDB(), "", "destroying")
	if err != nil {
		ctx.Logger().Warn("destroy recovery list", "err", err)
		return
	}
	for _, inst := range rows {
		if instanceCreateActive(ctx, inst.ID) {
			continue
		}
		if inst.CreatePending && inst.ProviderID == "" {
			_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"cleanup_error": "creation was interrupted before its provider ID was recorded; reconcile the provider resource before forgetting this row"})
			continue
		}
		if inst.CreatePending {
			_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"create_pending": false})
		}
		if err := resumeInstanceDestroy(ctx, inst); err != nil {
			ctx.Logger().Warn("destroy recovery pending", "id", inst.ID, "err", err)
		}
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
