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
