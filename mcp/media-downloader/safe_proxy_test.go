package main

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestSafeProxyDialBlocksPrivateResolution(t *testing.T) {
	proxy := &safeProxy{resolver: func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}}
	_, err := proxy.safeDialContext(context.Background(), "tcp", "example.com:443")
	if err == nil || !strings.Contains(err.Error(), "blocked address") {
		t.Fatalf("safe dial error = %v, want blocked address", err)
	}
}

func TestSafeProxyDialRejectsMissingPort(t *testing.T) {
	proxy := &safeProxy{resolver: defaultResolver}
	if _, err := proxy.safeDialContext(context.Background(), "tcp", "example.com"); err == nil {
		t.Fatal("expected missing port to fail")
	}
}
