package main

import (
	"context"
	"net"
	"testing"
)

func TestGuardURLBlocksPrivateResolution(t *testing.T) {
	resolver := func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	if err := guardURL(context.Background(), "https://example.com/watch", nil, nil, resolver); err == nil {
		t.Fatal("expected private IP to be blocked")
	}
}

func TestGuardURLAllowsPublicHTTP(t *testing.T) {
	resolver := func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}
	if err := guardURL(context.Background(), "https://www.youtube.com/watch?v=x", []string{"youtube.com"}, nil, resolver); err != nil {
		t.Fatal(err)
	}
}

func TestGuardURLRejectsDisallowedDomain(t *testing.T) {
	resolver := func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}
	if err := guardURL(context.Background(), "https://vimeo.com/1", []string{"youtube.com"}, nil, resolver); err == nil {
		t.Fatal("expected allowlist rejection")
	}
}
