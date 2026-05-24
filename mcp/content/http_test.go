package main

import (
	"net/http/httptest"
	"testing"
)

func TestComputeURLPrefix_ProxiedAppRequestKeepsAppMountWithForwardedHost(t *testing.T) {
	r := httptest.NewRequest("GET", "/home", nil)
	r.Header.Set("X-Apteva-App-Install-ID", "51")
	r.Header.Set("X-Forwarded-Host", "agents.schwartzindustries.com")

	if got := computeURLPrefix(r); got != "/api/apps/content/" {
		t.Fatalf("computeURLPrefix() = %q, want /api/apps/content/", got)
	}
}

func TestComputeURLPrefix_DomainLinkedRequestUsesRoot(t *testing.T) {
	r := httptest.NewRequest("GET", "/home", nil)
	r.Header.Set("X-Forwarded-Host", "clawengine.example.com")

	if got := computeURLPrefix(r); got != "/" {
		t.Fatalf("computeURLPrefix() = %q, want /", got)
	}
}

func TestResolveMenuURL_ProxiedPageKeepsProjectAndSite(t *testing.T) {
	r := httptest.NewRequest("GET", "/home?project_id=proj-1&site=clawengine", nil)
	r.Header.Set("X-Apteva-App-Install-ID", "51")

	got := resolveMenuURL(MenuItem{TargetKind: "page", TargetSlug: "pricing"}, computeURLPrefix(r), proxiedRenderQuery(r))
	want := "/api/apps/content/pricing?project_id=proj-1&site=clawengine"
	if got != want {
		t.Fatalf("resolveMenuURL() = %q, want %q", got, want)
	}
}

func TestResolveMenuURL_DomainLinkedPageStaysRootRelative(t *testing.T) {
	r := httptest.NewRequest("GET", "/home?project_id=proj-1&site=clawengine", nil)
	r.Header.Set("X-Forwarded-Host", "clawengine.example.com")

	got := resolveMenuURL(MenuItem{TargetKind: "page", TargetSlug: "pricing"}, computeURLPrefix(r), proxiedRenderQuery(r))
	if got != "/pricing" {
		t.Fatalf("resolveMenuURL() = %q, want /pricing", got)
	}
}
