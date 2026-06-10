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

func TestEmbeddedManifest_AllowsVerifiedVPSProviders(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if len(m.Requires.Integrations) != 1 {
		t.Fatalf("integrations = %d, want 1", len(m.Requires.Integrations))
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
