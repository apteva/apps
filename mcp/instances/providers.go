package main

import (
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const defaultProviderSlug = "hetzner"

var compatibleProviderSlugs = []string{
	"hetzner",
	"digitalocean",
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
	return fmt.Errorf("provider %q is compatible at the integration-binding layer, but the Instances %s adapter is not implemented yet; implemented provider adapters: hetzner, digitalocean, runpod", provider, operation)
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
		return providerAdapterUnavailable(inst.Provider, "destroy")
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
