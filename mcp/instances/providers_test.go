package main

import (
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type providerBindingPlatform struct {
	tk.BasePlatformClient
	slug string
}

type multiProviderBindingPlatform struct {
	tk.BasePlatformClient
	bindings  map[int64]string
	defaultID int64
}

func (p multiProviderBindingPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	ids := make([]any, 0, len(p.bindings))
	for _, id := range []int64{7, 8, 9} {
		if _, ok := p.bindings[id]; ok {
			ids = append(ids, float64(id))
		}
	}
	return &sdk.InstallIdentity{Bindings: map[string]any{
		"provider": map[string]any{"ids": ids, "default_id": float64(p.defaultID)},
	}}, nil
}

func (p multiProviderBindingPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: p.bindings[id]}, nil
}

func (p providerBindingPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"provider": float64(7)}}, nil
}

func (p providerBindingPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: p.slug}, nil
}

func TestResolveInstanceProvider_DefaultsToBoundProvider(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(providerBindingPlatform{slug: "digitalocean"}))

	got, err := resolveInstanceProvider(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "digitalocean" {
		t.Fatalf("provider = %q, want digitalocean", got)
	}
}

func TestResolveInstanceProvider_RejectsMismatchedExplicitProvider(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(providerBindingPlatform{slug: "linode"}))

	_, err := resolveInstanceProvider(ctx, "hetzner")
	if err == nil || !strings.Contains(err.Error(), `bound to "linode"`) {
		t.Fatalf("err = %v, want bound-provider mismatch", err)
	}
}

func TestResolveInstanceProvider_SelectsExplicitNonDefaultBinding(t *testing.T) {
	platform := multiProviderBindingPlatform{
		bindings:  map[int64]string{7: "digitalocean", 8: "hetzner"},
		defaultID: 8,
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))

	got, err := resolveInstanceProvider(ctx, "digitalocean")
	if err != nil {
		t.Fatal(err)
	}
	if got != "digitalocean" {
		t.Fatalf("provider = %q, want digitalocean", got)
	}
	bound, err := instanceProviderBinding(ctx, got)
	if err != nil {
		t.Fatal(err)
	}
	if bound.ConnectionID != 7 {
		t.Fatalf("connection id = %d, want 7", bound.ConnectionID)
	}
}

func TestBoundInstanceProviders_ReportsConfiguredDefault(t *testing.T) {
	platform := multiProviderBindingPlatform{
		bindings:  map[int64]string{7: "digitalocean", 8: "hetzner"},
		defaultID: 8,
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))

	providers := boundInstanceProviders(ctx)
	if len(providers) != 2 {
		t.Fatalf("providers = %#v, want 2", providers)
	}
	if providers[0].Provider != "digitalocean" || providers[0].Default {
		t.Fatalf("providers[0] = %#v", providers[0])
	}
	if providers[1].Provider != "hetzner" || !providers[1].Default {
		t.Fatalf("providers[1] = %#v", providers[1])
	}
}

func TestEmbeddedManifest_AllowsVerifiedVPSProviders(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if len(m.Requires.Integrations) != 1 {
		t.Fatalf("integrations = %d, want 1", len(m.Requires.Integrations))
	}
	if m.Requires.Integrations[0].Mode != "multiple" {
		t.Fatalf("provider integration mode = %q, want multiple", m.Requires.Integrations[0].Mode)
	}
	got := map[string]bool{}
	for _, slug := range m.Requires.Integrations[0].CompatibleSlugs {
		got[slug] = true
	}
	for _, slug := range compatibleProviderSlugs {
		if !got[slug] {
			t.Fatalf("manifest compatible_slugs missing %q", slug)
		}
	}
}
